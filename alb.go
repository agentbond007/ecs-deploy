package main

import (
	"github.com/aws/aws-sdk-go/aws"
	"github.com/aws/aws-sdk-go/aws/awserr"
	"github.com/aws/aws-sdk-go/aws/session"
	"github.com/aws/aws-sdk-go/service/acm"
	"github.com/aws/aws-sdk-go/service/elbv2"
	"github.com/juju/loggo"

	"errors"
	"strconv"
	"strings"
)

// logging
var albLogger = loggo.GetLogger("alb")

// ALB struct
type ALB struct {
	loadBalancerName string
	loadBalancerArn  string
	vpcId            string
	listeners        []*elbv2.Listener
	domain           string
	rules            map[string][]*elbv2.Rule
}

func (a *ALB) init(loadBalancerName string) error {
	a.loadBalancerName = loadBalancerName
	// retrieve vpcId and loadBalancerArn
	svc := elbv2.New(session.New())
	input := &elbv2.DescribeLoadBalancersInput{
		Names: []*string{
			aws.String(loadBalancerName),
		},
	}

	result, err := svc.DescribeLoadBalancers(input)
	if err != nil {
		if aerr, ok := err.(awserr.Error); ok {
			switch aerr.Code() {
			case elbv2.ErrCodeLoadBalancerNotFoundException:
				albLogger.Errorf(elbv2.ErrCodeLoadBalancerNotFoundException+": %v", aerr.Error())
			default:
				albLogger.Errorf(aerr.Error())
			}
		} else {
			// Print the error, cast err to awserr.Error to get the Code and
			// Message from an error.
			albLogger.Errorf(err.Error())
		}
		return errors.New("Could not describe loadbalancer")
	} else if len(result.LoadBalancers) == 0 {
		return errors.New("Could not describe loadbalancer (no elements returned)")
	}
	a.loadBalancerArn = *result.LoadBalancers[0].LoadBalancerArn
	a.loadBalancerName = *result.LoadBalancers[0].LoadBalancerName
	a.vpcId = *result.LoadBalancers[0].VpcId

	// get listeners
	err = a.getListeners()
	if err != nil {
		return err
	} else if len(result.LoadBalancers) == 0 {
		return errors.New("Could not get listeners for loadbalancer (no elements returned)")
	}
	// get domain (if SSL cert is attached)
	err = a.getDomainUsingCertificate()
	if err != nil {
		return err
	}

	return nil
}

// get the listeners for the loadbalancer
func (a *ALB) getListeners() error {
	svc := elbv2.New(session.New())
	input := &elbv2.DescribeListenersInput{LoadBalancerArn: aws.String(a.loadBalancerArn)}

	result, err := svc.DescribeListeners(input)
	if err != nil {
		if aerr, ok := err.(awserr.Error); ok {
			switch aerr.Code() {
			case elbv2.ErrCodeListenerNotFoundException:
				albLogger.Errorf(elbv2.ErrCodeListenerNotFoundException+": %v", aerr.Error())
			case elbv2.ErrCodeLoadBalancerNotFoundException:
				albLogger.Errorf(elbv2.ErrCodeLoadBalancerNotFoundException+": %v", aerr.Error())
			default:
				albLogger.Errorf(aerr.Error())
			}
		} else {
			albLogger.Errorf(err.Error())
		}
		return errors.New("Could not get Listeners for loadbalancer")
	}
	for _, l := range result.Listeners {
		a.listeners = append(a.listeners, l)
	}
	return nil
}

// get the domain using certificates
func (a *ALB) getDomainUsingCertificate() error {
	svc := acm.New(session.New())
	for _, l := range a.listeners {
		for _, c := range l.Certificates {
			albLogger.Debugf("ALB Certificate found with arn: %v", *c.CertificateArn)
			input := &acm.DescribeCertificateInput{
				CertificateArn: c.CertificateArn,
			}

			result, err := svc.DescribeCertificate(input)
			if err != nil {
				if aerr, ok := err.(awserr.Error); ok {
					switch aerr.Code() {
					case acm.ErrCodeResourceNotFoundException:
						albLogger.Errorf(acm.ErrCodeResourceNotFoundException+": %v", aerr.Error())
					case acm.ErrCodeInvalidArnException:
						albLogger.Errorf(acm.ErrCodeInvalidArnException+": %v", aerr.Error())
					default:
						albLogger.Errorf(aerr.Error())
					}
				} else {
					albLogger.Errorf(err.Error())
				}
				return errors.New("Could not describe certificate")
			}
			albLogger.Debugf("Domain found through ALB certificate: %v", *result.Certificate.DomainName)
			s := strings.Split(*result.Certificate.DomainName, ".")
			if len(s) >= 2 {
				a.domain = s[len(s)-2] + "." + s[len(s)-1]
			}
			return nil
		}
	}
	return nil
}

func (a *ALB) createTargetGroup(serviceName string, d Deploy) (*string, error) {
	svc := elbv2.New(session.New())
	input := &elbv2.CreateTargetGroupInput{
		Name:     aws.String(serviceName),
		VpcId:    aws.String(a.vpcId),
		Port:     aws.Int64(d.ServicePort),
		Protocol: aws.String(d.ServiceProtocol),
	}
	if d.HealthCheck.HealthyThreshold != 0 {
		input.SetHealthyThresholdCount(*aws.Int64(d.HealthCheck.HealthyThreshold))
	}
	if d.HealthCheck.UnhealthyThreshold != 0 {
		input.SetUnhealthyThresholdCount(*aws.Int64(d.HealthCheck.UnhealthyThreshold))
	}
	if d.HealthCheck.Path != "" {
		input.SetHealthCheckPath(*aws.String(d.HealthCheck.Path))
	}
	if d.HealthCheck.Port != "" {
		input.SetHealthCheckPort(*aws.String(d.HealthCheck.Port))
	}
	if d.HealthCheck.Protocol != "" {
		input.SetHealthCheckProtocol(*aws.String(d.HealthCheck.Protocol))
	}
	if d.HealthCheck.Interval != 0 {
		input.SetHealthCheckIntervalSeconds(*aws.Int64(d.HealthCheck.Interval))
	}
	if d.HealthCheck.Matcher != "" {
		input.SetMatcher(&elbv2.Matcher{HttpCode: aws.String(d.HealthCheck.Matcher)})
	}
	if d.HealthCheck.Timeout > 0 {
		input.SetHealthCheckTimeoutSeconds(*aws.Int64(d.HealthCheck.Timeout))
	}

	result, err := svc.CreateTargetGroup(input)
	if err != nil {
		if aerr, ok := err.(awserr.Error); ok {
			switch aerr.Code() {
			case elbv2.ErrCodeDuplicateTargetGroupNameException:
				albLogger.Errorf(elbv2.ErrCodeDuplicateTargetGroupNameException+": %v", aerr.Error())
			case elbv2.ErrCodeTooManyTargetGroupsException:
				albLogger.Errorf(elbv2.ErrCodeTooManyTargetGroupsException+": %v", aerr.Error())
			case elbv2.ErrCodeInvalidConfigurationRequestException:
				albLogger.Errorf(elbv2.ErrCodeInvalidConfigurationRequestException+": %v", aerr.Error())
			default:
				albLogger.Errorf(aerr.Error())
			}
		} else {
			// Print the error, cast err to awserr.Error to get the Code and
			// Message from an error.
			albLogger.Errorf(err.Error())
		}
		return nil, errors.New("Could not create target group")
	} else if len(result.TargetGroups) == 0 {
		return nil, errors.New("Could not create target group (target group list is empty)")
	}
	return result.TargetGroups[0].TargetGroupArn, nil
}

func (a *ALB) getHighestRule() (int64, error) {
	var highest int64
	svc := elbv2.New(session.New())

	input := &elbv2.DescribeRulesInput{ListenerArn: a.listeners[0].ListenerArn}

	c := true // parse more pages if c is true
	result, err := svc.DescribeRules(input)
	for c {
		if err != nil {
			if aerr, ok := err.(awserr.Error); ok {
				switch aerr.Code() {
				case elbv2.ErrCodeListenerNotFoundException:
					albLogger.Errorf(elbv2.ErrCodeListenerNotFoundException+": %v", aerr.Error())
				case elbv2.ErrCodeRuleNotFoundException:
					albLogger.Errorf(elbv2.ErrCodeRuleNotFoundException+": %v", aerr.Error())
				default:
					albLogger.Errorf(aerr.Error())
				}
			} else {
				// Print the error, cast err to awserr.Error to get the Code and
				// Message from an error.
				albLogger.Errorf(err.Error())
			}
			return 0, errors.New("Could not describe alb listener rules")
		}

		albLogger.Debugf("Looping rules: %+v", result.Rules)
		for _, rule := range result.Rules {
			if i, _ := strconv.ParseInt(*rule.Priority, 10, 64); i > highest {
				albLogger.Debugf("Found rule with priority: %d", i)
				highest = i
			}
		}
		if result.NextMarker == nil || len(*result.NextMarker) == 0 {
			c = false
		} else {
			input.SetMarker(*result.NextMarker)
			result, err = svc.DescribeRules(input)
		}
	}

	albLogger.Debugf("Higest rule: %d", highest)

	return highest, nil
}

func (a *ALB) createRuleForAllListeners(ruleType string, targetGroupArn string, rules []string, priority int64) ([]string, error) {
	var listeners []string
	for _, l := range a.listeners {
		err := a.createRule(ruleType, *l.ListenerArn, targetGroupArn, rules, priority)
		if err != nil {
			return nil, err
		}
		listeners = append(listeners, *l.ListenerArn)
	}
	return listeners, nil
}

func (a *ALB) createRuleForListeners(ruleType string, listeners []string, targetGroupArn string, rules []string, priority int64) ([]string, error) {
	var retListeners []string
	for _, l := range a.listeners {
		for _, l2 := range listeners {
			if l.Protocol != nil && strings.ToLower(*l.Protocol) == strings.ToLower(l2) {
				err := a.createRule(ruleType, *l.ListenerArn, targetGroupArn, rules, priority)
				if err != nil {
					return nil, err
				}
				retListeners = append(retListeners, *l.ListenerArn)
			}
		}
	}
	return retListeners, nil
}

func (a *ALB) createRule(ruleType string, listenerArn string, targetGroupArn string, rules []string, priority int64) error {
	svc := elbv2.New(session.New())
	input := &elbv2.CreateRuleInput{
		Actions: []*elbv2.Action{
			{
				TargetGroupArn: aws.String(targetGroupArn),
				Type:           aws.String("forward"),
			},
		},
		ListenerArn: aws.String(listenerArn),
		Priority:    aws.Int64(priority),
	}
	if ruleType == "pathPattern" {
		if len(rules) != 1 {
			return errors.New("Wrong number of rules (expected 1, got " + strconv.Itoa(len(rules)) + ")")
		}
		input.SetConditions([]*elbv2.RuleCondition{
			{
				Field:  aws.String("path-pattern"),
				Values: []*string{aws.String(rules[0])},
			},
		})
	} else if ruleType == "hostname" {
		if len(rules) != 1 {
			return errors.New("Wrong number of rules (expected 1, got " + strconv.Itoa(len(rules)) + ")")
		}
		hostname := rules[0] + "." + getEnv("LOADBALANCER_DOMAIN", a.domain)
		input.SetConditions([]*elbv2.RuleCondition{
			{
				Field:  aws.String("host-header"),
				Values: []*string{aws.String(hostname)},
			},
		})
	} else if ruleType == "combined" {
		if len(rules) != 2 {
			return errors.New("Wrong number of rules (expected 2, got " + strconv.Itoa(len(rules)) + ")")
		}
		hostname := rules[1] + "." + getEnv("LOADBALANCER_DOMAIN", a.domain)
		input.SetConditions([]*elbv2.RuleCondition{
			{
				Field:  aws.String("path-pattern"),
				Values: []*string{aws.String(rules[0])},
			},
			{
				Field:  aws.String("host-header"),
				Values: []*string{aws.String(hostname)},
			},
		})
	} else {
		return errors.New("ruleType not recognized: " + ruleType)
	}

	_, err := svc.CreateRule(input)
	if err != nil {
		if aerr, ok := err.(awserr.Error); ok {
			switch aerr.Code() {
			case elbv2.ErrCodePriorityInUseException:
				albLogger.Errorf(elbv2.ErrCodePriorityInUseException+": %v", aerr.Error())
			case elbv2.ErrCodeTooManyTargetGroupsException:
				albLogger.Errorf(elbv2.ErrCodeTooManyTargetGroupsException+": %v", aerr.Error())
			case elbv2.ErrCodeTooManyRulesException:
				albLogger.Errorf(elbv2.ErrCodeTooManyRulesException+": %v", aerr.Error())
			case elbv2.ErrCodeTargetGroupAssociationLimitException:
				albLogger.Errorf(elbv2.ErrCodeTargetGroupAssociationLimitException+": %v", aerr.Error())
			case elbv2.ErrCodeIncompatibleProtocolsException:
				albLogger.Errorf(elbv2.ErrCodeIncompatibleProtocolsException+": %v", aerr.Error())
			case elbv2.ErrCodeListenerNotFoundException:
				albLogger.Errorf(elbv2.ErrCodeListenerNotFoundException+": %v", aerr.Error())
			case elbv2.ErrCodeTargetGroupNotFoundException:
				albLogger.Errorf(elbv2.ErrCodeTargetGroupNotFoundException+": %v", aerr.Error())
			case elbv2.ErrCodeInvalidConfigurationRequestException:
				albLogger.Errorf(elbv2.ErrCodeInvalidConfigurationRequestException+": %v", aerr.Error())
			case elbv2.ErrCodeTooManyRegistrationsForTargetIdException:
				albLogger.Errorf(elbv2.ErrCodeTooManyRegistrationsForTargetIdException+": %v", aerr.Error())
			case elbv2.ErrCodeTooManyTargetsException:
				albLogger.Errorf(elbv2.ErrCodeTooManyTargetsException+": %v", aerr.Error())
			default:
				albLogger.Errorf(aerr.Error())
			}
		} else {
			// Print the error, cast err to awserr.Error to get the Code and
			// Message from an error.
			albLogger.Errorf(err.Error())
		}
		return errors.New("Could not create alb rule")
	}
	return nil
}

// get rules by listener
func (a *ALB) getRulesForAllListeners() error {
	a.rules = make(map[string][]*elbv2.Rule)
	svc := elbv2.New(session.New())

	for _, l := range a.listeners {
		input := &elbv2.DescribeRulesInput{ListenerArn: aws.String(*l.ListenerArn)}

		result, err := svc.DescribeRules(input)
		if err != nil {
			if aerr, ok := err.(awserr.Error); ok {
				switch aerr.Code() {
				case elbv2.ErrCodeListenerNotFoundException:
					albLogger.Errorf(elbv2.ErrCodeListenerNotFoundException+": %v", aerr.Error())
				case elbv2.ErrCodeRuleNotFoundException:
					albLogger.Errorf(elbv2.ErrCodeRuleNotFoundException+": %v", aerr.Error())
				default:
					albLogger.Errorf(aerr.Error())
				}
			} else {
				albLogger.Errorf(err.Error())
			}
			return errors.New("Could not get Listeners for loadbalancer")
		}
		for _, r := range result.Rules {
			a.rules[*l.ListenerArn] = append(a.rules[*l.ListenerArn], r)
			if len(r.Conditions) != 0 && len(r.Conditions[0].Values) != 0 {
				albLogger.Debugf("Importing rule: %+v", *r.Conditions[0].Values[0])
			}
		}
	}
	return nil
}
func (a *ALB) getTargetGroupArn(serviceName string) (*string, error) {
	svc := elbv2.New(session.New())
	input := &elbv2.DescribeTargetGroupsInput{
		Names: []*string{aws.String(serviceName)},
	}

	result, err := svc.DescribeTargetGroups(input)
	if err != nil {
		if aerr, ok := err.(awserr.Error); ok {
			switch aerr.Code() {
			case elbv2.ErrCodeLoadBalancerNotFoundException:
				albLogger.Errorf(elbv2.ErrCodeLoadBalancerNotFoundException+": %v", aerr.Error())
			case elbv2.ErrCodeTargetGroupNotFoundException:
				albLogger.Errorf(elbv2.ErrCodeTargetGroupNotFoundException+": %v", aerr.Error())
			default:
				albLogger.Errorf(aerr.Error())
			}
		} else {
			albLogger.Errorf(err.Error())
		}
		return nil, err
	}
	if len(result.TargetGroups) == 1 {
		return result.TargetGroups[0].TargetGroupArn, nil
	} else {
		if len(result.TargetGroups) == 0 {
			return nil, errors.New("No ALB target group found for service: " + serviceName)
		} else {
			return nil, errors.New("Multiple target groups found for service: " + serviceName + " (" + string(len(result.TargetGroups)) + ")")
		}
	}
}
func (a *ALB) getDomain() string {
	return getEnv("LOADBALANCER_DOMAIN", a.domain)
}
func (a *ALB) findRule(listener string, targetGroupArn string, conditionField []string, conditionValue []string) (*string, *string, error) {
	if len(conditionField) != len(conditionValue) {
		return nil, nil, errors.New("conditionField length not equal to conditionValue length")
	}
	// examine rules
	if rules, ok := a.rules[listener]; ok {
		for _, r := range rules {
			for _, a := range r.Actions {
				if *a.Type == "forward" && *a.TargetGroupArn == targetGroupArn {
					// target group found, loop over conditions
					priorityFound := false
					skip := false
					for _, c := range r.Conditions {
						match := false
						for i, _ := range conditionField {
							if *c.Field == conditionField[i] && len(c.Values) > 0 && *c.Values[0] == conditionValue[i] {
								match = true
							}
						}
						if !skip && match { // if any condition was false, skip this rule
							priorityFound = true
						} else {
							priorityFound = false
							skip = true
						}
					}
					if priorityFound {
						return r.RuleArn, r.Priority, nil
					}
				}
			}
		}
	} else {
		return nil, nil, errors.New("Listener not found in rule list")
	}
	return nil, nil, errors.New("Priority not found for rule: listener " + listener + ", targetGroupArn: " + targetGroupArn + ", Field: " + strings.Join(conditionField, ",") + ", Value: " + strings.Join(conditionValue, ","))
}
