/*
Copyright 2024.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package controller

import (
	"context"
	"fmt"
	"time"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	v1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/tools/record"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/log"

	pagerdutyv1beta1 "github.com/mattgialelis/dutycontroller/api/v1beta1"
	"github.com/mattgialelis/dutycontroller/pkg/condtions"
	"github.com/mattgialelis/dutycontroller/pkg/providers/pd"
)

const (
	serviceFinalizer = "service.dutycontroller.io/finalizer"
)

// ServicesReconciler reconciles a Services object
type ServicesReconciler struct {
	client.Client
	Scheme      *runtime.Scheme
	PagerClient *pd.Pagerduty
	recorder    record.EventRecorder
}

//+kubebuilder:rbac:groups=pagerduty.dutycontroller.io,resources=services,verbs=get;list;watch;create;update;patch;delete
//+kubebuilder:rbac:groups=pagerduty.dutycontroller.io,resources=services/status,verbs=get;update;patch
//+kubebuilder:rbac:groups=pagerduty.dutycontroller.io,resources=services/finalizers,verbs=update

func (r *ServicesReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	log := log.FromContext(ctx)
	log.WithValues("service", req.NamespacedName)
	var service pagerdutyv1beta1.Services

	// Fetch the Service instance
	if err := r.Get(ctx, req.NamespacedName, &service); err != nil {
		//do not requeue if the resource does not exist
		if apierrors.IsNotFound(err) {
			return ctrl.Result{}, nil
		}
		return ctrl.Result{}, fmt.Errorf("get resource: %w", err)
	}

	s := client.MergeFrom(service.DeepCopy())
	// Defer update of Service status
	defer func() {
		if service.DeletionTimestamp.IsZero() {
			if err := r.Status().Patch(ctx, &service, s); err != nil {
				log.Error(err, "unable to update Service status")
			}
		}
	}()

	// Get Conditions
	// We do this here so we can use the condtions status in the rest of the function
	createdCondition := condtions.GetCondition(service.Status.Conditions, condtions.ConditionReasonCreated)
	busServiceCondition := condtions.GetCondition(service.Status.Conditions, condtions.ConditionReasonAssociated)

	// Check if the Service instance is marked for deletion
	if service.DeletionTimestamp.IsZero() {
		// Check and add finalizer for deletion
		if !controllerutil.ContainsFinalizer(&service, serviceFinalizer) {
			controllerutil.AddFinalizer(&service, serviceFinalizer)

			if err := r.Update(ctx, &service); err != nil {
				return ctrl.Result{}, fmt.Errorf("could not update finalizers: %w", err)
			}
		}
	} else {
		if controllerutil.ContainsFinalizer(&service, serviceFinalizer) {

			log.Info("Deleting Service", "ID", service.Status.ID, "Name", service.Name)

			if createdCondition != nil && createdCondition.Status == v1.ConditionTrue {
				err := r.PagerClient.DeletePagerDutyService(ctx, service.Status.ID)
				if err != nil {
					return ctrl.Result{}, fmt.Errorf("could not delete pagerduty service: %w", err)
				}
			}

			// Remove the finalizer
			controllerutil.RemoveFinalizer(&service, serviceFinalizer)
			if err := r.Client.Update(ctx, &service, &client.UpdateOptions{}); err != nil {
				return ctrl.Result{}, fmt.Errorf("could not update finalizers: %w", err)
			}

			return ctrl.Result{}, nil
		}
	}

	// Get BusinessService ID
	businessServiceId, err := r.getBusinessServiceId(ctx, &service)
	if err != nil {
		return ctrl.Result{}, fmt.Errorf("get business service id: %w", err)
	}

	// Get Escalation Policy ID
	escalationPolicyId, err := r.getEscalationPolicyId(&service)
	if err != nil {
		return ctrl.Result{}, fmt.Errorf("get escalation policy id: %w", err)
	}

	// Convert CRD to PagerDuty service struct
	pagerService := pd.ServicesSpectoService(service, escalationPolicyId)

	//Check if the Service instance exists
	_, exists, err := r.PagerClient.GetPagerDutyServiceByName(ctx, req.Name, true)
	if err != nil && exists {
		return ctrl.Result{}, fmt.Errorf("could not get service by name: %w", err)
	}

	if !exists {
		// Create Service
		id, err := r.PagerClient.CreatePagerDutyService(ctx, pagerService)
		if err != nil {
			return ctrl.Result{}, fmt.Errorf("could not create service: %w", err)
		}

		if createdCondition == nil || createdCondition.Status != v1.ConditionTrue {
			condtions.SetCondition(&service.Status.Conditions, condtions.ConditionReasonCreated, v1.ConditionTrue, "Service created successfully in PagerDuty")
		}

		service.Status.ID = id
		log.Info("Created Service", "ID", id)
	} else {
		if createdCondition != nil && createdCondition.Status == v1.ConditionTrue {
			// Update Service
			err = r.PagerClient.UpdatePagerDutyService(ctx, pagerService)
			if err != nil {
				return ctrl.Result{}, fmt.Errorf("could not update service: %w", err)
			}
			log.Info("Updated Service", "ID", service.Status.ID)
		} else {
			log.Info("Service already exists in Pagerduty", "ID", service.Status.ID)
			condtions.SetCondition(&service.Status.Conditions, condtions.ConditionReasonFailed, v1.ConditionTrue, "Service cannot be created, already exists in PagerDuty")
			return ctrl.Result{}, nil
		}
	}

	//TODO: add a check to see if the business service is associated with the service
	//TODO: add a check to see if the business service chanaged
	if businessServiceId != "" && busServiceCondition == nil || busServiceCondition != nil && busServiceCondition.Status != v1.ConditionTrue {
		time.Sleep(1 * time.Second)
		err := r.PagerClient.AssociateServiceBusiness(service.Status.ID, businessServiceId)
		if err != nil {
			log.Error(err, "could not associate service with business service", "businessSeriviceId", businessServiceId, "serviceId", service.Status.ID)
			return ctrl.Result{}, fmt.Errorf("could not associate service with business service: %w", err)
		}

		log.Info("Associated Service with BusinessService", "BusinessServiceID", businessServiceId, "ServiceID", service.Status.ID)
		condtions.SetCondition(&service.Status.Conditions, condtions.ConditionReasonAssociated, v1.ConditionTrue, "Service associated with BusinessService")
		return ctrl.Result{}, nil
	}

	return ctrl.Result{}, nil
}

// SetupWithManager sets up the controller with the Manager.
func (r *ServicesReconciler) SetupWithManager(mgr ctrl.Manager) error {
	r.recorder = mgr.GetEventRecorderFor("services-controller")

	return ctrl.NewControllerManagedBy(mgr).
		For(&pagerdutyv1beta1.Services{}).
		WithOptions(controller.Options{MaxConcurrentReconciles: 10}).
		Complete(r)
}

func (r *ServicesReconciler) getEscalationPolicyId(service *pagerdutyv1beta1.Services) (string, error) {
	escalationPolicyId, _, err := r.PagerClient.GetEscalationPolicyByName(service.Spec.EscalationPolicy)
	if err != nil {
		return "", fmt.Errorf("could not get escalation policy by name: %w", err)
	}
	return escalationPolicyId, nil
}

func (r *ServicesReconciler) getBusinessServiceId(ctx context.Context, service *pagerdutyv1beta1.Services) (string, error) {
	log := log.FromContext(ctx)

	if service.Spec.BusinessService != "" {
		var businessService pagerdutyv1beta1.BusinessService
		if err := r.Get(ctx, client.ObjectKey{
			Name: service.Spec.BusinessService,
		}, &businessService); err != nil {
			if apierrors.IsNotFound(err) {
				log.Info("BusinessService not found in cluster, fetching from PagerDuty", "namespace", service.Namespace, "name", service.Spec.BusinessService)
				businessServiceId, _, err := r.PagerClient.GetBusinessServicebyName(ctx, service.Spec.BusinessService, true)
				if err != nil {
					return "", fmt.Errorf("could not get business service by name: %w", err)
				}
				log.Info("BusinessService found in PagerDuty", "service", businessServiceId)
				return businessServiceId, nil
			}
			return "", fmt.Errorf("get BusinessService: %w", err)
		}
		log.Info("BusinessService found in cluster", "service", businessService.Status.ID)
		return businessService.Status.ID, nil
	}
	return "", nil
}
