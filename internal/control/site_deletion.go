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

type SiteDeletionManager struct {
	Store        *store.Store
	Publisher    Publisher
	DNS          integrations.DNSProvider
	Certificates integrations.CertificateCleaner

	mu sync.Mutex
}

func (m *SiteDeletionManager) Start(ctx context.Context, siteID, actor, remoteAddr string) (domain.PublishStatus, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	_, task, _, err := m.Store.BeginSiteDeletion(siteID, actor, remoteAddr, time.Now().UTC().Add(90*time.Second))
	if err != nil {
		return domain.PublishStatus{}, err
	}
	if err := m.advance(ctx, task.ID); err != nil {
		_ = m.Store.FailSiteDeletion(task.ID, err.Error())
		status, _ := m.Store.SiteDeletionStatus(siteID)
		return status, err
	}
	return m.Store.SiteDeletionStatus(siteID)
}

func (m *SiteDeletionManager) Reconcile(ctx context.Context) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	jobs, err := m.Store.ActiveSiteDeletionJobs()
	if err != nil {
		return err
	}
	for _, job := range jobs {
		if err := m.advance(ctx, job.TaskID); err != nil {
			_ = m.Store.FailSiteDeletion(job.TaskID, err.Error())
			return err
		}
	}
	return nil
}

func (m *SiteDeletionManager) Status(ctx context.Context, siteID string) (domain.PublishStatus, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	task, err := m.Store.LatestSiteDeletionTask(siteID)
	if errors.Is(err, store.ErrNotFound) {
		return domain.PublishStatus{}, nil
	}
	if err != nil {
		return domain.PublishStatus{}, err
	}
	if task.Status == domain.TaskQueued || task.Status == domain.TaskDispatching || task.Status == domain.TaskApplying {
		if err := m.advance(ctx, task.ID); err != nil {
			_ = m.Store.FailSiteDeletion(task.ID, err.Error())
		}
	}
	return m.Store.SiteDeletionStatus(siteID)
}

func (m *SiteDeletionManager) advance(ctx context.Context, taskID string) error {
	for range 4 {
		job, err := m.Store.SiteDeletionJobForTask(taskID)
		if errors.Is(err, store.ErrNotFound) {
			return nil
		}
		if err != nil {
			return err
		}
		switch job.Phase {
		case store.SiteDeletionWithdrawingDNS:
			if m.DNS == nil {
				return errors.New("DNS provider is not configured")
			}
			if err := m.Store.SetSiteDeletionPhase(taskID, store.SiteDeletionWithdrawingDNS, domain.TaskDispatching, "withdrawing managed DNS records"); err != nil {
				return err
			}
			site, zoneID, err := m.Store.GetSite(job.SiteID)
			if err != nil {
				return err
			}
			if err := m.DNS.Reconcile(ctx, zoneID, "site="+site.ID, nil); err != nil {
				return fmt.Errorf("withdraw managed DNS for %s: %w", site.Name, err)
			}
			updates, targets, err := m.Publisher.PrepareSiteRemoval(site.ID)
			if err != nil {
				return fmt.Errorf("prepare edge removal for %s: %w", site.Name, err)
			}
			if err := m.Store.StageSiteDeletion(taskID, updates, targets); err != nil {
				return err
			}
		case store.SiteDeletionWaitingForEdges:
			ready, err := m.Store.SiteDeletionReady(taskID)
			if err != nil {
				return err
			}
			if !ready {
				return nil
			}
		case store.SiteDeletionFinalizing:
			if m.Certificates == nil {
				return errors.New("certificate cleanup is not configured")
			}
			if err := m.Certificates.Delete(ctx, "site-"+job.SiteID); err != nil {
				return fmt.Errorf("delete site certificate lineage: %w", err)
			}
			return m.Store.CompleteSiteDeletion(taskID)
		default:
			return fmt.Errorf("unknown site deletion phase %q", job.Phase)
		}
	}
	return errors.New("site deletion did not converge")
}
