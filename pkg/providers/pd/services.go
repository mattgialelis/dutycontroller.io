package pd

import (
	"context"
	"fmt"

	"sigs.k8s.io/controller-runtime/pkg/log"

	"strings"

	pagerdutyv1beta1 "github.com/mattgialelis/dutycontroller/api/v1beta1"

	"github.com/PagerDuty/go-pagerduty"
)

type Service struct {
	Name               string
	Description        string
	Status             string
	EscalationPolicyID string
	AutoResolveTimeout int
	AcknowedgeTimeout  int
}

func ServicesSpectoService(bs pagerdutyv1beta1.Services, EscalationPolicyID string) Service {
	return Service{
		Name:               bs.Name,
		Description:        bs.Spec.Description,
		Status:             bs.Spec.Status,
		EscalationPolicyID: EscalationPolicyID,
		AutoResolveTimeout: bs.Spec.AutoResolveTimeout,
		AcknowedgeTimeout:  bs.Spec.AcknowedgeTimeout,
	}
}

func (s *Service) ToPagerDutyService() pagerduty.Service {
	return pagerduty.Service{
		Name:        s.Name,
		Description: s.Description,
		EscalationPolicy: pagerduty.EscalationPolicy{
			APIObject: pagerduty.APIObject{
				ID:   s.EscalationPolicyID,
				Type: "escalation_policy_reference",
			},
		},
	}
}

func (p *Pagerduty) CreatePagerDutyService(ctx context.Context, service Service) (string, error) {
	log := log.FromContext(ctx)

	serviceInput := service.ToPagerDutyService()

	newService, err := p.client.CreateServiceWithContext(context.TODO(), serviceInput)

	if err != nil {
		return "", fmt.Errorf("failed to create service: %w", err)
	}

	// Force a refresh of the cache after successful update
	err = p.ForceRefreshServiceCache()
	if err != nil {
		// Log the error but don't return it, as the update was successful
		log.Info("Warning: Failed to refresh service cache after update:", "Warning", err)
	}

	return newService.ID, nil
}

func (p *Pagerduty) UpdatePagerDutyService(ctx context.Context, service Service) error {
	log := log.FromContext(ctx)

	existingService, _, err := p.GetPagerDutyServiceByName(context.TODO(), service.Name, true)
	if err != nil {
		return err
	}

	// Check if any relevant fields have changed
	if !needsUpdate(existingService, service) {
		log.Info("No changes detected for service. Skipping update.", "serviceName:", service.Name)
		return nil
	}

	serviceInput := service.ToPagerDutyService()
	serviceInput.ID = existingService.ID

	_, err = p.client.UpdateServiceWithContext(context.TODO(), serviceInput)
	if err != nil {
		return fmt.Errorf("failed to update service: %w", err)
	}

	// Force a refresh of the cache after successful update
	err = p.ForceRefreshServiceCache()
	if err != nil {
		// Log the error but don't return it, as the update was successful
		log.Info("Warning: Failed to refresh service cache after update:", "Warning", err)
	}

	return nil
}

// needsUpdate compares the existing service with the new service data
// and returns true if any relevant fields have changed
func needsUpdate(existing pagerduty.Service, new Service) bool {
	if existing.Name != new.Name {
		return true
	}
	if existing.Description != new.Description {
		return true
	}
	if existing.EscalationPolicy.ID != new.EscalationPolicyID {
		return true
	}
	return false
}

func (p *Pagerduty) DeletePagerDutyService(ctx context.Context, id string) error {
	log := log.FromContext(ctx)

	err := p.client.DeleteServiceWithContext(context.Background(), id)
	if err != nil {
		if strings.Contains(err.Error(), "404") {
			log.Info("Service not found in PagerDdy, skipping deletion, no need to delete")
			return nil
		} else {
			return fmt.Errorf("failed to delete service: %w", err)
		}
	}

	// Force a refresh of the cache after successful update
	err = p.ForceRefreshServiceCache()
	if err != nil {
		// Log the error but don't return it, as the update was successful
		log.Info("Warning: Failed to refresh service cache after delete", "Warning", err)
	}

	return nil
}

func (p *Pagerduty) GetPagerDutyServiceByName(ctx context.Context, name string, useCache bool) (pagerduty.Service, bool, error) {
	log := log.FromContext(ctx)

	if useCache {
		serviceCache, err := p.GetServiceCache()
		if err != nil {
			switch err.(type) {
			case *CacheNotRegistered:
				log.Info("ServiceCache not registered, falling back to direct PagerDuty call")
			case *CacheEmpty:
				log.Info("ServiceCache is empty, falling back to direct PagerDuty call")
			default:
				log.Info("Error retrieving ServiceCache, falling back to direct PagerDuty call", "Warning", err)
			}
		} else {
			for _, svc := range serviceCache.cache {
				if svc.Name == name {
					return svc, true, nil
				}
			}

			log.Info("Service not found in ServiceCache, falling back to direct PagerDuty call")
		}
	}

	return p.GetPagerDutyServiceByNameDirect(ctx, name)
}

func (p *Pagerduty) GetPagerDutyServiceByNameDirect(ctx context.Context, name string) (pagerduty.Service, bool, error) {
	// Go directly to PagerDuty to get the service
	var allServices []pagerduty.Service
	var offset uint = 0

	log := log.FromContext(ctx)

	for {
		services, err := p.client.ListServicesPaginated(
			context.Background(),
			pagerduty.ListServiceOptions{Limit: 100, Offset: offset},
		)
		if err != nil {
			log.Info("Failed to refresh PagerDuty service cache", "Warning", err)
			return pagerduty.Service{}, false, err
		}
		allServices = append(allServices, services...)
		if len(services) < 100 {
			break
		}
		offset += 100
	}

	for _, svc := range allServices {
		if svc.Name == name {
			return svc, true, nil
		}
	}

	return pagerduty.Service{}, false, nil
}
