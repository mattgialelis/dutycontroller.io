package pd

import (
	"bytes"
	"context"
	"errors"
	"io"

	"sigs.k8s.io/controller-runtime/pkg/log"

	"net/http"
	"net/url"

	"github.com/PagerDuty/go-pagerduty"
	pagerdutyv1beta1 "github.com/mattgialelis/dutycontroller/api/v1beta1"
	"github.com/sirupsen/logrus"
)

type OrchestrationRoute struct {
	EventOrchestrationName string
	Label                  string
	Expression             []string
	RouteTo                string
}

func ServiceRouteToOrchestrationRoute(serviceID string, Route pagerdutyv1beta1.ServiceRoute) OrchestrationRoute {
	return OrchestrationRoute{
		EventOrchestrationName: Route.EventOrchestration,
		Label:                  Route.Label,
		Expression:             Route.Conditions,
		RouteTo:                serviceID,
	}
}

func (or OrchestrationRoute) ConditionsFromOrchestrationRoute() []*pagerduty.OrchestrationRouterRuleCondition {
	conditions := make([]*pagerduty.OrchestrationRouterRuleCondition, 0)
	for _, e := range or.Expression {
		conditions = append(conditions, &pagerduty.OrchestrationRouterRuleCondition{
			Expression: e,
		})
	}

	return conditions
}

func (or OrchestrationRoute) RuleFromOrchestrationRoute() *pagerduty.OrchestrationRouterRule {
	return &pagerduty.OrchestrationRouterRule{
		Label:      or.Label,
		Conditions: or.ConditionsFromOrchestrationRoute(),
		Actions: &pagerduty.OrchestrationRouterActions{
			RouteTo: or.RouteTo,
		},
	}
}

func (or *OrchestrationRoute) GenerateUpdateFromOrchestrationRouterRule(rule *pagerduty.OrchestrationRouterRule) {
	if or.Label == "" {
		or.Label = rule.Label
	}
	if or.RouteTo == "" {
		or.RouteTo = rule.Actions.RouteTo
	}
	if len(or.Expression) == 0 {
		for _, c := range rule.Conditions {
			or.Expression = append(or.Expression, c.Expression)
		}
	}
}

// Run a check given the EventOrchestrationName and serviceID to see if the route exists and reutrn true if it does
func (pd *Pagerduty) DoesRouteExist(EventOrchestrationName string, serviceID string) (bool, error) {
	orchID, ok, err := pd.GetEventOrchestrationByName(EventOrchestrationName)
	if err != nil {
		return false, err
	} else {
		if !ok {
			return false, errors.New("orchestration not found")
		}
	}

	router, err := pd.client.GetOrchestrationRouterWithContext(context.Background(), orchID, &pagerduty.GetOrchestrationRouterOptions{})
	if err != nil {
		return false, err
	}

	for _, rule := range router.Sets[0].Rules {
		if rule.Actions.RouteTo == serviceID {
			return true, nil
		}
	}

	return false, nil
}

// Get Orchestration by Name
func (pd *Pagerduty) GetEventOrchestrationByName(name string) (string, bool, error) {
	orch, err := pd.client.ListOrchestrationsWithContext(context.Background(), pagerduty.ListOrchestrationsOptions{})
	if err != nil {
		return "", false, err
	}

	for _, o := range orch.Orchestrations {
		if o.Name == name {
			return o.ID, true, nil
		}
	}

	return "", false, nil
}

// Add new Rule to the existing Orchestrations
func (pd *Pagerduty) AddOrchestrationServiceRoute(OrchestrationRoute OrchestrationRoute) error {
	orchID, ok, err := pd.GetEventOrchestrationByName(OrchestrationRoute.EventOrchestrationName)
	if err != nil {
		return err
	} else {
		if !ok {
			return errors.New("orchestration not found")
		}
	}

	//Load existing Routes for the Orchestrations
	router, err := pd.client.GetOrchestrationRouterWithContext(context.Background(), orchID, &pagerduty.GetOrchestrationRouterOptions{})
	if err != nil {
		return err
	}

	rule := OrchestrationRoute.RuleFromOrchestrationRoute()

	//Add new Rule to the existing Orchestrations
	router.Sets[0].Rules = append(router.Sets[0].Rules, rule)

	//Apply the new Rule to the Orchestrations
	_, err = pd.client.UpdateOrchestrationRouterWithContext(context.Background(), orchID, *router)
	if err != nil {
		return err
	}

	return nil
}

func (pd *Pagerduty) UpdateOrchestrationServiceRoute(OrchestrationRoute OrchestrationRoute) error {
	orchID, ok, err := pd.GetEventOrchestrationByName(OrchestrationRoute.EventOrchestrationName)
	if err != nil {
		return err
	} else {
		if !ok {
			return errors.New("orchestration not found")
		}
	}

	//Load existing Routes for the Orchestration
	router, err := pd.client.GetOrchestrationRouterWithContext(context.Background(), orchID, &pagerduty.GetOrchestrationRouterOptions{})
	if err != nil {
		return err
	}

	updated := false
	for i, rule := range router.Sets[0].Rules {
		if rule.Actions.RouteTo == OrchestrationRoute.RouteTo {

			OrchestrationRoute.GenerateUpdateFromOrchestrationRouterRule(router.Sets[0].Rules[i])

			if !routesEqual(*router.Sets[0].Rules[i], *OrchestrationRoute.RuleFromOrchestrationRoute()) {
				router.Sets[0].Rules[i] = OrchestrationRoute.RuleFromOrchestrationRoute()
				updated = true
			}
		}
	}

	if !updated {
		log.Log.Info("No changes to orchestration route", "route", OrchestrationRoute.RouteTo)
		return nil
	}

	_, err = pd.client.UpdateOrchestrationRouterWithContext(context.Background(), orchID, *router)
	if err != nil {
		return err
	}

	return nil
}

// routesEqual compares two OrchestrationRoutes and returns true if they are equal
func routesEqual(a, b pagerduty.OrchestrationRouterRule) bool {
	// Compare Labels
	if a.Label != b.Label {
		return false
	}

	// Compare RouteTo actions
	if a.Actions == nil || b.Actions == nil {
		return a.Actions == b.Actions
	}
	if a.Actions.RouteTo != b.Actions.RouteTo {
		return false
	}

	// Compare Conditions
	if len(a.Conditions) != len(b.Conditions) {
		return false
	}
	for i := range a.Conditions {
		if a.Conditions[i].Expression != b.Conditions[i].Expression {
			return false
		}
	}

	return true
}

func (pd *Pagerduty) DeleteOrchestrationServiceRoute(ctx context.Context, OrchestrationRoute OrchestrationRoute) error {
	log := log.FromContext(ctx)

	orchID, ok, err := pd.GetEventOrchestrationByName(OrchestrationRoute.EventOrchestrationName)
	if err != nil {
		return err
	} else {
		if !ok {
			return errors.New("orchestration not found")
		}
	}

	//Load existing Routes for the Orchestrations
	router, err := pd.client.GetOrchestrationRouterWithContext(context.Background(), orchID, &pagerduty.GetOrchestrationRouterOptions{})
	if err != nil {
		return err
	}

	newRules := []*pagerduty.OrchestrationRouterRule{} // Initialize an empty slice
	for _, rule := range router.Sets[0].Rules {
		if rule.Actions.RouteTo != OrchestrationRoute.RouteTo {
			newRules = append(newRules, rule)
		} else {
			log.Info("Removing route", "route", rule.Actions.RouteTo)
		}
	}

	router.Sets[0].Rules = newRules

	if len(newRules) == 0 {
		err = pd.clearEventOrchestration(orchID)
		if err != nil {
			return err
		}
	} else {
		_, err = pd.client.UpdateOrchestrationRouterWithContext(context.Background(), orchID, *router)
		if err != nil {
			return err
		}
	}

	return nil
}

// This Custom function is needed as the pagerduty module does not allow setting the rules to an empty array,
// due to it being a pointer type as well as omitempty in the struct
func (pd *Pagerduty) clearEventOrchestration(id string) error {
	payload := `
	{
		"orchestration_path": {
			"catch_all": {
				"actions": {
					"route_to": "unrouted"
				}
			},
			"sets": [
				{
					"id": "start",
					"rules": []
				}
			],
			"type": "router"
		}
	}
`
	url, err := url.Parse("https://api.pagerduty.com/event_orchestrations/" + id + "/router")
	if err != nil {
		return err
	}

	req := http.Request{
		Method: http.MethodPut,
		URL:    url,
		Body:   io.NopCloser(bytes.NewReader([]byte(payload))),
		Header: http.Header{
			"Content-Type":  []string{"application/json"},
			"Authorization": []string{"Token token=" + pd.apiKey},
		},
	}

	resp, err := pd.client.HTTPClient.Do(&req)
	if err != nil {
		return err
	}

	if resp.StatusCode != http.StatusOK {
		logrus.Error(resp.StatusCode)
		logrus.Info(resp.Body)
		return errors.New("unexpected status code")
	}
	logrus.Error(resp.StatusCode)

	return nil
}
