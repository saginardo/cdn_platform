package control

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"cdn-platform/internal/domain"
	"cdn-platform/internal/integrations"
	"cdn-platform/internal/store"
)

type CertificateManager struct {
	Store     *store.Store
	Publisher Publisher
	Issuer    integrations.CertificateIssuer
	Notifier  integrations.Notifier

	IssueTimeout time.Duration

	mu       sync.Mutex
	started  bool
	stopped  bool
	ctx      context.Context
	cancel   context.CancelFunc
	jobs     chan certificateJob
	done     chan struct{}
	stopOnce sync.Once
}

type certificateJob struct {
	TaskID string
	SiteID string
	Kind   string
}

const (
	manualCertificateTask = "issue_certificate"
	renewCertificateTask  = "renew_certificate"
)

// Start establishes the worker lifetime independently of individual HTTP
// requests. It is safe to call repeatedly with the same manager.
func (m *CertificateManager) Start(ctx context.Context) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.started || m.stopped {
		return
	}
	m.ctx, m.cancel = context.WithCancel(ctx)
	m.jobs = make(chan certificateJob, 32)
	m.done = make(chan struct{})
	m.started = true
	go m.worker(m.ctx, m.jobs, m.done)
}

// Stop cancels the single certificate worker, waits for Certbot to exit, then
// makes any queued or interrupted work visible as failed instead of replaying
// it after a control-plane restart.
func (m *CertificateManager) Stop() {
	m.stopOnce.Do(func() {
		m.mu.Lock()
		if !m.started {
			m.stopped = true
			m.mu.Unlock()
			return
		}
		m.stopped = true
		cancel, done := m.cancel, m.done
		m.mu.Unlock()
		cancel()
		<-done
		_, _ = m.Store.FailActiveCertificateTasks("certificate issuance interrupted by control-plane shutdown; retry Issue TLS")
	})
}

func (m *CertificateManager) Run(ctx context.Context) {
	m.Start(ctx)
	ticker := time.NewTicker(12 * time.Hour)
	defer ticker.Stop()
	for {
		m.Reconcile(ctx)
		select {
		case <-ctx.Done():
			m.Stop()
			return
		case <-ticker.C:
		}
	}
}

func (m *CertificateManager) Reconcile(ctx context.Context) {
	if m.Issuer == nil {
		return
	}
	sites, err := m.Store.ListSites()
	if err != nil {
		return
	}
	for _, site := range sites {
		if !site.Enabled || !site.Published {
			continue
		}
		_, _, notAfter, certificateErr := m.Store.Certificate(site.ID)
		if certificateErr == nil && notAfter != nil && notAfter.After(time.Now().UTC().Add(30*24*time.Hour)) {
			continue
		}
		if _, _, err := m.QueueRenewal(site); err != nil && m.Notifier != nil {
			_ = m.Notifier.Notify(ctx, "CDN alert: certificate renewal failed", "Site "+site.Name+": "+err.Error())
		}
	}
}

func (m *CertificateManager) QueueIssue(site domain.Site) (domain.DeploymentTask, bool, error) {
	return m.enqueue(manualCertificateTask, site.ID, "queued for DNS-01 certificate issuance")
}

func (m *CertificateManager) QueueRenewal(site domain.Site) (domain.DeploymentTask, bool, error) {
	return m.enqueue(renewCertificateTask, site.ID, "queued for certificate renewal")
}

func (m *CertificateManager) enqueue(kind, siteID, detail string) (domain.DeploymentTask, bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if !m.started || m.stopped {
		return domain.DeploymentTask{}, false, errors.New("certificate manager is not running")
	}
	task, created, err := m.Store.CreateOrGetActiveCertificateTask(kind, siteID, detail)
	if err != nil || !created {
		return task, created, err
	}
	job := certificateJob{TaskID: task.ID, SiteID: siteID, Kind: kind}
	select {
	case m.jobs <- job:
		return task, true, nil
	default:
		_ = m.Store.UpdateTask(task.ID, domain.TaskFailed, "certificate queue is full; retry Issue TLS")
		return task, true, errors.New("certificate queue is full")
	}
}

func (m *CertificateManager) worker(ctx context.Context, jobs <-chan certificateJob, done chan<- struct{}) {
	defer close(done)
	for {
		select {
		case <-ctx.Done():
			return
		case job := <-jobs:
			m.runJob(ctx, job)
		}
	}
}

func (m *CertificateManager) runJob(ctx context.Context, job certificateJob) {
	if ctx.Err() != nil {
		_ = m.Store.UpdateTask(job.TaskID, domain.TaskFailed, "certificate issuance interrupted by control-plane shutdown; retry Issue TLS")
		return
	}
	if err := m.Store.UpdateTask(job.TaskID, domain.TaskDispatching, "preparing DNS-01 certificate issuance"); err != nil {
		return
	}
	site, _, err := m.Store.GetSite(job.SiteID)
	if err != nil {
		_ = m.Store.UpdateTask(job.TaskID, domain.TaskFailed, err.Error())
		return
	}
	if err := m.Store.UpdateTask(job.TaskID, domain.TaskApplying, "waiting for DNS-01 validation"); err != nil {
		return
	}
	timeout := m.IssueTimeout
	if timeout <= 0 {
		timeout = 10 * time.Minute
	}
	issueCtx, cancel := context.WithTimeout(ctx, timeout)
	err = m.issue(issueCtx, site)
	timedOut := errors.Is(issueCtx.Err(), context.DeadlineExceeded)
	cancel()
	if ctx.Err() != nil {
		err = errors.New("certificate issuance interrupted by control-plane shutdown; retry Issue TLS")
	} else if err != nil && timedOut {
		err = fmt.Errorf("certificate issuance timed out after %s", timeout)
	}
	if err != nil {
		_ = m.Store.UpdateTask(job.TaskID, domain.TaskFailed, err.Error())
		m.notifyRenewalFailure(ctx, job, site, err)
		return
	}
	if job.Kind == renewCertificateTask {
		// Mark renewal success before creating its publication task so TLS status
		// can use one chronological rule for first issuance and renewal.
		if err := m.Store.UpdateTask(job.TaskID, domain.TaskSucceeded, "certificate renewed"); err != nil {
			return
		}
		if _, err := m.Publisher.PublishSite(site.ID); err != nil {
			_ = m.Store.UpdateTask(job.TaskID, domain.TaskFailed, err.Error())
			m.notifyRenewalFailure(ctx, job, site, err)
			return
		}
		return
	}
	_ = m.Store.UpdateTask(job.TaskID, domain.TaskSucceeded, "certificate stored; publish the site to deploy it")
}

func (m *CertificateManager) notifyRenewalFailure(ctx context.Context, job certificateJob, site domain.Site, err error) {
	if job.Kind == renewCertificateTask && m.Notifier != nil {
		_ = m.Notifier.Notify(ctx, "CDN alert: certificate renewal failed", "Site "+site.Name+": "+err.Error())
	}
}

func (m *CertificateManager) issue(ctx context.Context, site domain.Site) error {
	if m.Issuer == nil {
		return fmt.Errorf("certificate issuer is not configured")
	}
	certificate, err := m.Issuer.Issue(ctx, "site-"+site.ID, site.Domains)
	if err != nil {
		return err
	}
	if err := validateCertificateDomains(certificate.CertificatePEM, site.Domains, time.Now().UTC()); err != nil {
		return fmt.Errorf("issuer returned unsuitable certificate: %w", err)
	}
	if err := m.Publisher.StoreCertificate(site.ID, certificate.CertificatePEM, certificate.PrivateKeyPEM, certificate.NotAfter); err != nil {
		return err
	}
	return nil
}
