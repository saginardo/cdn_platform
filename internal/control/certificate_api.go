package control

import (
	"errors"
	"net/http"
	"time"

	"simple_cdn/internal/domain"
	"simple_cdn/internal/store"
)

type certificateOverviewResponse struct {
	RenewalWindowDays        int                     `json:"renewal_window_days"`
	ReconcileIntervalSeconds int                     `json:"reconcile_interval_seconds"`
	Sites                    []certificateSiteStatus `json:"sites"`
}

type certificateSiteStatus struct {
	SiteID                    string                 `json:"site_id"`
	SiteName                  string                 `json:"site_name"`
	Domains                   []string               `json:"domains"`
	Enabled                   bool                   `json:"enabled"`
	Published                 bool                   `json:"published"`
	Deleting                  bool                   `json:"deleting"`
	NeedsCertificate          bool                   `json:"needs_certificate"`
	CertificatePresent        bool                   `json:"certificate_present"`
	CertificateUpdatedAt      *time.Time             `json:"certificate_updated_at,omitempty"`
	NotAfter                  *time.Time             `json:"not_after,omitempty"`
	RenewalDueAt              *time.Time             `json:"renewal_due_at,omitempty"`
	PublishedAfterCertificate bool                   `json:"published_after_certificate"`
	Task                      *domain.DeploymentTask `json:"task"`
}

func (s *Server) certificatesOverview(response http.ResponseWriter, _ *http.Request) {
	sites, err := s.Store.ListSites()
	if err != nil {
		writeError(response, http.StatusInternalServerError, err)
		return
	}
	result := certificateOverviewResponse{
		RenewalWindowDays:        int(certificateRenewalWindow / (24 * time.Hour)),
		ReconcileIntervalSeconds: int(certificateReconcileInterval / time.Second),
		Sites:                    make([]certificateSiteStatus, 0, len(sites)),
	}
	for _, site := range sites {
		status := certificateSiteStatus{
			SiteID:           site.ID,
			SiteName:         site.Name,
			Domains:          append([]string(nil), site.Domains...),
			Enabled:          site.Enabled,
			Published:        site.Published,
			Deleting:         site.Deleting,
			NeedsCertificate: domain.SiteNeedsCertificate(site),
		}
		metadata, metadataErr := s.Store.CertificateMetadata(site.ID)
		if metadataErr == nil {
			status.CertificatePresent = true
			updatedAt := metadata.UpdatedAt
			status.CertificateUpdatedAt = &updatedAt
			status.NotAfter = metadata.NotAfter
			if metadata.NotAfter != nil {
				renewalDueAt := metadata.NotAfter.Add(-certificateRenewalWindow)
				status.RenewalDueAt = &renewalDueAt
			}
			status.PublishedAfterCertificate, err = s.Store.HasSuccessfulPublishAfter(site.ID, metadata.UpdatedAt)
			if err != nil {
				writeError(response, http.StatusInternalServerError, err)
				return
			}
		} else if !errors.Is(metadataErr, store.ErrNotFound) {
			writeError(response, http.StatusInternalServerError, metadataErr)
			return
		}
		task, taskErr := s.Store.LatestCertificateTask(site.ID)
		if taskErr == nil {
			status.Task = &task
		} else if !errors.Is(taskErr, store.ErrNotFound) {
			writeError(response, http.StatusInternalServerError, taskErr)
			return
		}
		result.Sites = append(result.Sites, status)
	}
	writeJSON(response, http.StatusOK, result)
}

func (s *Server) renewCertificate(response http.ResponseWriter, request *http.Request) {
	if s.CertificateManager == nil {
		writeError(response, http.StatusNotImplemented, errors.New("certificate issuer is not configured"))
		return
	}
	site, _, err := s.Store.GetSite(request.PathValue("id"))
	if err != nil {
		writeStoreError(response, err)
		return
	}
	if !domain.SiteNeedsCertificate(site) {
		writeError(response, http.StatusConflict, errors.New("site has no TLS listeners"))
		return
	}
	if _, err := s.Store.CertificateMetadata(site.ID); errors.Is(err, store.ErrNotFound) {
		writeError(response, http.StatusConflict, errors.New("site has no certificate; issue one first"))
		return
	} else if err != nil {
		writeError(response, http.StatusInternalServerError, err)
		return
	}
	task, created, err := s.CertificateManager.QueueRenewal(site)
	if err != nil {
		if errors.Is(err, store.ErrSiteDeleting) {
			writeError(response, http.StatusConflict, err)
			return
		}
		writeError(response, http.StatusServiceUnavailable, err)
		return
	}
	detail := task.ID
	if !created {
		detail += " reused"
	}
	s.audit(request, adminID(request.Context()), "renew_certificate", "site", site.ID, detail)
	writeJSON(response, http.StatusAccepted, task)
}
