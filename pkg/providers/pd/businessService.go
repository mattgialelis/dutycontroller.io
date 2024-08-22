package pd

import (
	"context"
	"fmt"

	"github.com/PagerDuty/go-pagerduty"
	"sigs.k8s.io/controller-runtime/pkg/log"

	pagerdutyv1beta1 "github.com/mattgialelis/dutycontroller/api/v1beta1"
)

type BusinessService struct {
	Name           string `json:"name"`
	Description    string `json:"description"`
	PointOfContact string `json:"pointOfContact"`
	TeamId         string `json:"team"`
}

// TeamID must be passed in as in the  pagerdutyv1beta1.BusinessService we are using the Team name,
// this allows us to look up the team ID either via a kube resource or a direct call to the pagerduty API
func BusinessServiceSpectoBusinessService(bs pagerdutyv1beta1.BusinessService, teamId string) BusinessService {
	return BusinessService{
		Name:           bs.Name,
		Description:    bs.Spec.Description,
		PointOfContact: bs.Spec.PointOfContact,
		TeamId:         teamId,
	}
}

func (b *BusinessService) ToPagerDutyBusinessService() *pagerduty.BusinessService {
	return &pagerduty.BusinessService{
		Name:           b.Name,
		Description:    b.Description,
		PointOfContact: b.PointOfContact,
		Team: &pagerduty.BusinessServiceTeam{
			ID: b.TeamId,
		},
	}
}

// GetBusinessServicebyName returns the business service ID by name
// Input:
//
//	name:  Name of the business service
func (pd *Pagerduty) GetBusinessServicebyName(ctx context.Context, name string, useCache bool) (string, bool, error) {
	log := log.FromContext(ctx)

	if useCache {
		businessServiceCache, err := pd.GetBusinessServiceCache()
		if err != nil {
			switch err.(type) {
			case *CacheNotRegistered:
				log.Info("BusinessService not registered, falling back to direct PagerDuty call")
			case *CacheEmpty:
				log.Info("BusinessService is empty, falling back to direct PagerDuty call")
			default:
				log.Info("Error retrieving BusinessService, falling back to direct PagerDuty call: %v", err)
			}
		} else {
			for _, bs := range businessServiceCache.cache {
				if bs.Name == name {
					return bs.ID, true, nil
				}
			}
			log.Info("BusinessService not found in Cache, falling back to direct PagerDuty call", "BusinessService", name)
		}
	}

	bservices, err := pd.client.ListBusinessServicesPaginated(context.Background(), pagerduty.ListBusinessServiceOptions{})
	if err != nil {
		return "", false, err
	}

	for _, s := range bservices {
		if s.Name == name {
			return s.ID, true, nil
		}
	}

	return "", false, fmt.Errorf("businessService not found")
}

// Creates a business service
// Input:
//
//	businessService:  BusinessService struct with the values
func (pd *Pagerduty) CreateBusinessService(businessService BusinessService) (string, error) {

	input := businessService.ToPagerDutyBusinessService()

	bservice, err := pd.client.CreateBusinessServiceWithContext(context.Background(), input)
	if err != nil {
		return "", err
	}

	return bservice.ID, nil
}

// Updates a business service
// Input:
//
//	businessService:  BusinessService struct with the updated values
func (pd *Pagerduty) UpdateBusinessService(ctx context.Context, businessService BusinessService) error {

	id, _, err := pd.GetBusinessServicebyName(ctx, businessService.Name, true)
	if err != nil {
		return err
	}

	input := businessService.ToPagerDutyBusinessService()
	input.ID = id

	_, err = pd.client.UpdateBusinessServiceWithContext(context.Background(), input)
	if err != nil {
		return err
	}

	return nil
}

// Deletes a business service
// Input:
//
//	name:  Name of the business service
func (pd *Pagerduty) DeleteBusinessService(id string) error {

	err := pd.client.DeleteBusinessServiceWithContext(context.Background(), id)
	if err != nil {
		return err
	}

	return nil
}

// AssociateServiceBusiness associates a service with a business service
func (pd *Pagerduty) AssociateServiceBusiness(serviceID, business_service string) error {

	input := pagerduty.ListServiceDependencies{
		Relationships: []*pagerduty.ServiceDependency{
			{
				SupportingService: &pagerduty.ServiceObj{
					ID:   serviceID,
					Type: "service",
				},
				DependentService: &pagerduty.ServiceObj{
					ID:   business_service,
					Type: "business_service",
				},
			},
		}}

	_, err := pd.client.AssociateServiceDependenciesWithContext(context.Background(), &input)

	if err != nil {
		return err
	}

	return nil
}
