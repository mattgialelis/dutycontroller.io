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

	"math/rand"

	"k8s.io/apimachinery/pkg/util/json"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/tools/record"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/log"

	pagerdutyv1beta1 "github.com/mattgialelis/dutycontroller/api/v1beta1"
	"github.com/mattgialelis/dutycontroller/pkg/providers/pd"
)

const (
	orchestrationRouteFinalizer = "orchestrationroute.dutycontroller.io/finalizer"
)

// OrchestrationroutesReconciler reconciles a Orchestrationroutes object
type OrchestrationroutesReconciler struct {
	client.Client
	Scheme      *runtime.Scheme
	PagerClient *pd.Pagerduty
	recorder    record.EventRecorder
}

//+kubebuilder:rbac:groups=pagerduty.dutycontroller.io,resources=orchestrationroutes,verbs=get;list;watch;create;update;patch;delete
//+kubebuilder:rbac:groups=pagerduty.dutycontroller.io,resources=orchestrationroutes/status,verbs=get;update;patch
//+kubebuilder:rbac:groups=pagerduty.dutycontroller.io,resources=orchestrationroutes/finalizers,verbs=update

func (r *OrchestrationroutesReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	log := log.FromContext(ctx)
	log.WithValues("orchestrationRoutes", req.NamespacedName)

	requeueAfterTimer := RandomRequeueTimer()

	var orchestrationRoute pagerdutyv1beta1.Orchestrationroutes

	// Fetch the Orchestratoion instance
	if err := r.Get(ctx, req.NamespacedName, &orchestrationRoute); err != nil {
		//do not requeue if the resource does not exist
		if apierrors.IsNotFound(err) {
			return ctrl.Result{}, nil
		}
		return ctrl.Result{}, fmt.Errorf("get resource: %w", err)
	}

	// Check if the Orchestratoion instance is marked for deletion
	if orchestrationRoute.DeletionTimestamp.IsZero() {
		if !controllerutil.ContainsFinalizer(&orchestrationRoute, orchestrationRouteFinalizer) {
			controllerutil.AddFinalizer(&orchestrationRoute, orchestrationRouteFinalizer)
			if err := r.Update(ctx, &orchestrationRoute); err != nil {
				return ctrl.Result{}, fmt.Errorf("could not update finalizers: %w", err)
			}
			return ctrl.Result{}, nil
		}
	} else {
		if controllerutil.ContainsFinalizer(&orchestrationRoute, orchestrationRouteFinalizer) {
			//Delete all routes
			for _, route := range orchestrationRoute.Spec.ServiceRoutes {
				serviceID, err := r.LookupService(ctx, req.Namespace, route.ServiceRef)
				if err != nil {
					log.Error(err, "could not find the service")
					return ctrl.Result{}, nil
				}

				orhcestrationRoute := pd.ServiceRouteToOrchestrationRoute(serviceID, route)

				err = r.PagerClient.DeleteOrchestrationServiceRoute(ctx, orhcestrationRoute)
				if err != nil {
					log.Error(err, "could not delete orchestration route")
					return ctrl.Result{}, fmt.Errorf("couldnt delete orchestration route: %w", err)
				}
			}

			// Remove the finalizer
			controllerutil.RemoveFinalizer(&orchestrationRoute, orchestrationRouteFinalizer)
			if err := r.Client.Update(ctx, &orchestrationRoute, &client.UpdateOptions{}); err != nil {
				return ctrl.Result{}, fmt.Errorf("could not update finalizers: %w", err)
			}

			return ctrl.Result{}, nil
		}
	}

	s := client.MergeFrom(orchestrationRoute.DeepCopy())
	// Defer update of Service status
	defer func() {
		if err := r.Status().Patch(ctx, &orchestrationRoute, s); err != nil {
			log.Error(err, "unable to update Service status")
		}
	}()

	maxRoutes := len(orchestrationRoute.Spec.ServiceRoutes)

	// Create Orchestration route.
	for routeIndex, route := range orchestrationRoute.Spec.ServiceRoutes {
		//Lookup Service
		serviceID, err := r.LookupService(ctx, req.Namespace, route.ServiceRef)
		if err != nil {

			log.Info(fmt.Sprintf("could not find the service, re-queuing in %s", requeueAfterTimer), "service", route.ServiceRef, "error", err)
			return ctrl.Result{RequeueAfter: requeueAfterTimer}, nil
		}

		//Check if Route exists
		exists, err := r.PagerClient.DoesRouteExist(route.EventOrchestration, serviceID)
		if err != nil {
			return ctrl.Result{}, fmt.Errorf("could not check if route exists: %w", err)
		}

		orhcestrationRoute := pd.ServiceRouteToOrchestrationRoute(serviceID, route)

		if !exists {
			log.Info("Route does not exist, creating route", "route", orhcestrationRoute)
			err := r.PagerClient.AddOrchestrationServiceRoute(orhcestrationRoute)
			if err != nil {
				return ctrl.Result{}, fmt.Errorf("error creating Orchestration route: %w", err)
			}
		} else {
			// Unmarshal last applied routes
			lastAppleidRoutes := []pagerdutyv1beta1.ServiceRoute{}
			if orchestrationRoute.Status.LastAppliedRoutes != "" {
				err := json.Unmarshal([]byte(orchestrationRoute.Status.LastAppliedRoutes), &lastAppleidRoutes)
				if err != nil {
					return ctrl.Result{}, fmt.Errorf("error unmarshalling last applied routes: %w", err)
				}

				// Compare routes
				//TODO: Move this outside of the loop and only do it once, or compare just this route to what exists
				removedRoutes := CompareRoutes(lastAppleidRoutes, orchestrationRoute.Spec.ServiceRoutes)

				for _, removedRoute := range removedRoutes {
					serivceIdToRemove, err := r.LookupService(ctx, req.Namespace, removedRoute.ServiceRef)
					if err != nil {
						return ctrl.Result{}, fmt.Errorf("could not find the service: %w", err)
					}

					err = r.PagerClient.DeleteOrchestrationServiceRoute(ctx, pd.ServiceRouteToOrchestrationRoute(serivceIdToRemove, removedRoute))
					if err != nil {
						return ctrl.Result{}, fmt.Errorf("could not delete orchestration route: %w", err)
					}
				}
			}

			log.Info("Route exists, updating route", "route", orhcestrationRoute)
			err = r.PagerClient.UpdateOrchestrationServiceRoute(orhcestrationRoute)
			if err != nil {
				return ctrl.Result{}, fmt.Errorf("error updating Orchestration route: %w", err)
			}
		}

		if routeIndex == maxRoutes-1 {
			routesApplied, err := json.Marshal(orchestrationRoute.Spec.ServiceRoutes)
			if err != nil {
				return ctrl.Result{}, fmt.Errorf("error marshalling json into Last applied routes")
			}

			orchestrationRoute.Status.LastAppliedRoutes = string(routesApplied)
			return ctrl.Result{}, nil
		}
	}

	return ctrl.Result{}, nil
}

// SetupWithManager sets up the controller with the Manager.
func (r *OrchestrationroutesReconciler) SetupWithManager(mgr ctrl.Manager) error {
	r.recorder = mgr.GetEventRecorderFor("orchestrationroutes-controller")
	return ctrl.NewControllerManagedBy(mgr).
		For(&pagerdutyv1beta1.Orchestrationroutes{}).
		WithOptions(controller.Options{MaxConcurrentReconciles: 1}).
		Complete(r)
}

func (r *OrchestrationroutesReconciler) LookupService(ctx context.Context, namespace string, serviceName string) (string, error) {
	var service pagerdutyv1beta1.Services
	log := log.FromContext(ctx)

	log.Info("Looking up service", "namespace", namespace, "serviceName", serviceName)

	if err := r.Get(ctx, client.ObjectKey{
		Namespace: namespace,
		Name:      serviceName,
	}, &service); err != nil {
		if apierrors.IsNotFound(err) {
			log.Info("Service not found in cluster, fetching from PagerDuty", "namespace", namespace, "name", serviceName)

			pagerDutyService, exists, err := r.PagerClient.GetPagerDutyServiceByName(ctx, serviceName, true)
			if err != nil || !exists {
				return "", fmt.Errorf("could not get  service by name: %w", err)
			}

			log.Info("Service found in PagerDuty or Kubernetes", "service", pagerDutyService)

			return pagerDutyService.ID, nil
		}
	}

	if service.Status.ID != "" {
		log.Info("Service found in cluster", "service", service.Status.ID)
		return service.Status.ID, nil
	}

	return "", fmt.Errorf("could not find service with ID")
}

func CompareRoutes(oldRoutes, newRoutes []pagerdutyv1beta1.ServiceRoute) []pagerdutyv1beta1.ServiceRoute {
	// Store removed routes
	var removedRoutes []pagerdutyv1beta1.ServiceRoute

	// Loop through old routes
	for _, oldRoute := range oldRoutes {
		found := false

		// Check if route exists in new routes
		for _, newRoute := range newRoutes {
			if oldRoute.EventOrchestration == newRoute.EventOrchestration &&
				oldRoute.Label == newRoute.Label &&
				oldRoute.ServiceRef == newRoute.ServiceRef {
				found = true
				break
			}
		}

		// If not found, it's removed
		if !found {
			removedRoutes = append(removedRoutes, oldRoute)
		}
	}

	return removedRoutes
}

func RandomRequeueTimer() time.Duration {

	rand.New(rand.NewSource(time.Now().UnixNano()))

	// Define the minimum and maximum durations in seconds
	minDuration := 2 * 60 // 2 minutes in seconds
	maxDuration := 5 * 60 // 5 minutes in seconds

	// Generate a random duration between minDuration and maxDuration
	randomDurationInSeconds := rand.Intn(maxDuration-minDuration+1) + minDuration

	// Convert seconds to time.Duration
	return time.Duration(randomDurationInSeconds) * time.Second
}
