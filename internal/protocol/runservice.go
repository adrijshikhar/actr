package protocol

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// StepReference represents an action reference in a workflow job step.
type StepReference struct {
	ID      string `json:"id,omitempty"`
	Name    string `json:"name"`
	Version string `json:"version,omitempty"`
	Ref     string `json:"ref,omitempty"`
	Type    string `json:"type"`
	Path    string `json:"path,omitempty"`
}

// JobStep represents a single step within an acquired job specification.
type JobStep struct {
	ID               string            `json:"id"`
	Name             string            `json:"name"`
	Type             string            `json:"type"` // "script", "run", "uses"
	Script           string            `json:"script,omitempty"`
	WorkingDirectory string            `json:"workingDirectory,omitempty"`
	Environment      map[string]string `json:"environment,omitempty"`
	Inputs           map[string]string `json:"inputs,omitempty"`
	Reference        *StepReference    `json:"reference,omitempty"`
	Condition        string            `json:"condition,omitempty"`
}

// Step is an alias for JobStep for convenience.
type Step = JobStep

// JobContainer defines the container execution environment for a workflow job.
type JobContainer struct {
	Image       string            `json:"image"`
	Options     string            `json:"options,omitempty"`
	Environment map[string]string `json:"environment,omitempty"`
	Volumes     []string          `json:"volumes,omitempty"`
	Ports       []string          `json:"ports,omitempty"`
}

// EndpointAuthorization represents authentication scheme and parameters for a service endpoint.
type EndpointAuthorization struct {
	Scheme     string            `json:"scheme"`
	Parameters map[string]string `json:"parameters"`
}

type rawEndpointAuth = EndpointAuthorization

// ServiceEndpoint represents an external or internal service endpoint.
type ServiceEndpoint struct {
	Name          string                 `json:"name"`
	Type          string                 `json:"type"`
	URL           string                 `json:"url"`
	Authorization *EndpointAuthorization `json:"authorization,omitempty"`
	Data          map[string]string      `json:"data,omitempty"`
}

// JobSpecification represents the complete workflow job payload returned by the Run Service.
type JobSpecification struct {
	ID               string            `json:"id,omitempty"`
	JobID            string            `json:"jobId"`
	JobName          string            `json:"jobName"`
	PlanID           string            `json:"planId"`
	JobToken         string            `json:"jobToken,omitempty"`
	Steps            []JobStep         `json:"steps"`
	Variables        map[string]string `json:"variables,omitempty"`
	Environment      map[string]string `json:"environment,omitempty"`
	ContextData      map[string]any    `json:"contextData,omitempty"`
	Container        *JobContainer     `json:"container,omitempty"`
	Endpoints        []ServiceEndpoint `json:"endpoints,omitempty"`
	EventName        string            `json:"eventName,omitempty"`
	IsLocalExecution bool              `json:"isLocalExecution,omitempty"`
}

type rawStepReference struct {
	ID      string `json:"id"`
	Name    string `json:"name"`
	Version string `json:"version"`
	Ref     string `json:"ref"`
	Type    string `json:"type"`
	Path    string `json:"path"`
}

type rawStep struct {
	ID               string            `json:"id"`
	Name             any               `json:"name"`
	DisplayName      any               `json:"displayName"`
	DisplayNameToken any               `json:"displayNameToken"`
	ContextName      string            `json:"contextName"`
	Type             string            `json:"type"`
	Script           any               `json:"script"`
	WorkingDirectory any               `json:"workingDirectory"`
	Environment      any               `json:"environment"`
	Reference        *rawStepReference `json:"reference"`
	Inputs           any               `json:"inputs"`
	Condition        any               `json:"condition"`
	ConditionAlt     any               `json:"Condition"`
	If               any               `json:"if"`
	IfAlt            any               `json:"If"`
}

type rawPlan struct {
	PlanID   string `json:"planId"`
	PlanType string `json:"planType"`
	ID       string `json:"id"`
}

type rawEndpoint struct {
	Name          string           `json:"name"`
	Type          string           `json:"type"`
	URL           string           `json:"url"`
	Authorization *rawEndpointAuth `json:"authorization"`
	Data          map[string]any   `json:"data"`
}

type rawRepository struct {
	Alias   string `json:"alias"`
	ID      string `json:"id"`
	Name    string `json:"name"`
	URL     string `json:"url"`
	Version string `json:"version"`
}

type rawContainer struct {
	Alias       string `json:"alias"`
	Image       any    `json:"image"`
	Options     any    `json:"options"`
	Environment any    `json:"environment"`
	Env         any    `json:"env"`
	Volumes     any    `json:"volumes"`
	Ports       any    `json:"ports"`
}

type rawJobResources struct {
	Endpoints    []rawEndpoint   `json:"endpoints"`
	Repositories []rawRepository `json:"repositories"`
	Containers   []rawContainer  `json:"containers"`
}

type rawJobSpec struct {
	ID                   string           `json:"id"`
	JobID                string           `json:"jobId"`
	JobIDAlt             string           `json:"job_id"`
	JobName              any              `json:"jobName"`
	JobDisplayName       any              `json:"jobDisplayName"`
	Name                 any              `json:"name"`
	PlanID               string           `json:"planId"`
	Plan                 *rawPlan         `json:"plan"`
	Steps                []rawStep        `json:"steps"`
	Variables            map[string]any   `json:"variables"`
	EnvironmentVariables any              `json:"environmentVariables"`
	Environment          any              `json:"environment"`
	Endpoints            []rawEndpoint    `json:"endpoints"`
	ContextData          map[string]any   `json:"contextData"`
	Resources            *rawJobResources `json:"resources"`
	Container            any              `json:"container"`
	EventName            string           `json:"eventName"`
	IsLocalExecution     bool             `json:"isLocalExecution"`
}

func extractASTString(v any) string {
	if v == nil {
		return ""
	}
	switch val := v.(type) {
	case string:
		return val
	case map[string]any:
		if lit, ok := val["lit"]; ok && lit != nil {
			return fmt.Sprint(lit)
		}
		if expr, ok := val["expr"]; ok && expr != nil {
			var s string
			switch e := expr.(type) {
			case string:
				s = e
			case map[string]any:
				s = extractASTString(e)
			default:
				s = fmt.Sprint(e)
			}
			if s != "" && !strings.HasPrefix(s, "${{") && !strings.HasPrefix(s, "format(") {
				return "${{ " + s + " }}"
			}
			return s
		}
		if expr, ok := val["expression"]; ok && expr != nil {
			var s string
			switch e := expr.(type) {
			case string:
				s = e
			case map[string]any:
				s = extractASTString(e)
			default:
				s = fmt.Sprint(e)
			}
			if s != "" && !strings.HasPrefix(s, "${{") && !strings.HasPrefix(s, "format(") {
				return "${{ " + s + " }}"
			}
			return s
		}
		if tVal, ok := val["t"]; ok && (tVal == 1 || tVal == float64(1) || tVal == "1" || fmt.Sprint(tVal) == "1") {
			vStr := extractASTString(val["v"])
			if vStr == "" {
				vStr = extractASTString(val["val"])
			}
			if vStr != "" && !strings.HasPrefix(vStr, "${{") && !strings.HasPrefix(vStr, "format(") {
				return "${{ " + vStr + " }}"
			}
			return vStr
		}
		if v, ok := val["v"]; ok && v != nil {
			return extractASTString(v)
		}
		if valStr, ok := val["val"]; ok && valStr != nil {
			return extractASTString(valStr)
		}
		if value, ok := val["value"]; ok && value != nil {
			return extractASTString(value)
		}
		if Value, ok := val["Value"]; ok && Value != nil {
			return extractASTString(Value)
		}
		return ""
	default:
		return fmt.Sprint(val)
	}
}

func extractConditionString(v any) string {
	if v == nil {
		return ""
	}
	var s string
	switch val := v.(type) {
	case bool:
		return strconv.FormatBool(val)
	case int:
		return strconv.Itoa(val)
	case int64:
		return strconv.FormatInt(val, 10)
	case float64:
		return strconv.FormatFloat(val, 'f', -1, 64)
	case string:
		s = strings.TrimSpace(val)
	case map[string]any:
		s = strings.TrimSpace(extractASTString(val))
	default:
		return ""
	}
	if strings.HasPrefix(s, "${{") && strings.HasSuffix(s, "}}") {
		s = strings.TrimSpace(s[3 : len(s)-2])
	}
	return s
}

func extractASTMap(v any) map[string]string {
	if v == nil {
		return nil
	}
	res := make(map[string]string)
	switch val := v.(type) {
	case map[string]string:
		for k, item := range val {
			res[k] = item
		}
		return res
	case map[string]any:
		// Check if it has a "map" slice (GitHub Pipeline AST dictionary)
		if mapSlice, ok := val["map"].([]any); ok {
			for _, item := range mapSlice {
				if itemMap, ok := item.(map[string]any); ok {
					k := extractASTString(itemMap["key"])
					if k == "" {
						k = extractASTString(itemMap["Key"])
					}
					if k == "" {
						k = extractASTString(itemMap["k"])
					}
					vStr := extractASTString(itemMap["value"])
					if vStr == "" {
						vStr = extractASTString(itemMap["Value"])
					}
					if vStr == "" {
						vStr = extractASTString(itemMap["v"])
					}
					if vStr == "" {
						vStr = extractASTString(itemMap["val"])
					}
					if k != "" {
						res[k] = vStr
					}
				}
			}
			return res
		}
		// Regular map[string]any
		for k, item := range val {
			res[k] = extractASTString(item)
		}
		return res
	default:
		return nil
	}
}

func extractEnvironmentVariables(v any) map[string]string {
	if v == nil {
		return nil
	}
	res := make(map[string]string)
	if slice, ok := v.([]any); ok {
		for _, item := range slice {
			if itemMap, ok := item.(map[string]any); ok {
				k := extractASTString(itemMap["key"])
				if k == "" {
					k = extractASTString(itemMap["Key"])
				}
				if k == "" {
					k = extractASTString(itemMap["k"])
				}
				if k != "" {
					vStr := extractASTString(itemMap["value"])
					if vStr == "" {
						vStr = extractASTString(itemMap["Value"])
					}
					if vStr == "" {
						vStr = extractASTString(itemMap["v"])
					}
					if vStr == "" {
						vStr = extractASTString(itemMap["val"])
					}
					res[k] = vStr
					continue
				}
			}
			m := extractASTMap(item)
			for k, val := range m {
				res[k] = val
			}
		}
		if len(res) > 0 {
			return res
		}
	}
	return extractASTMap(v)
}

func extractASTSlice(v any) []string {
	if v == nil {
		return nil
	}
	switch val := v.(type) {
	case []string:
		if len(val) == 0 {
			return nil
		}
		return val
	case []any:
		res := make([]string, 0, len(val))
		for _, item := range val {
			str := extractASTString(item)
			if str != "" {
				res = append(res, str)
			}
		}
		if len(res) == 0 {
			return nil
		}
		return res
	case string:
		str := strings.TrimSpace(val)
		if str != "" {
			return []string{str}
		}
		return nil
	case map[string]any:
		if arrSlice, ok := val["array"].([]any); ok {
			res := make([]string, 0, len(arrSlice))
			for _, item := range arrSlice {
				str := extractASTString(item)
				if str != "" {
					res = append(res, str)
				}
			}
			if len(res) == 0 {
				return nil
			}
			return res
		}
		return nil
	default:
		return nil
	}
}

func parseRawContainer(c *rawContainer) *JobContainer {
	if c == nil {
		return nil
	}
	img := extractASTString(c.Image)
	if img == "" {
		return nil
	}
	env := extractASTMap(c.Environment)
	if len(env) == 0 {
		env = extractASTMap(c.Env)
	}
	if len(env) == 0 {
		env = nil
	}
	return &JobContainer{
		Image:       img,
		Options:     extractASTString(c.Options),
		Environment: env,
		Volumes:     extractASTSlice(c.Volumes),
		Ports:       extractASTSlice(c.Ports),
	}
}

func parseContainerFromAny(v any) *JobContainer {
	if v == nil {
		return nil
	}
	switch val := v.(type) {
	case *JobContainer:
		return val
	case JobContainer:
		return &val
	case string:
		trimmed := strings.TrimSpace(val)
		if trimmed == "" {
			return nil
		}
		return &JobContainer{Image: trimmed}
	case map[string]any:
		var imageVal, optionsVal, envVal, volumesVal, portsVal any
		if mapSlice, ok := val["map"].([]any); ok {
			for _, item := range mapSlice {
				if itemMap, ok := item.(map[string]any); ok {
					k := strings.ToLower(extractASTString(itemMap["key"]))
					if k == "" {
						k = strings.ToLower(extractASTString(itemMap["Key"]))
					}
					vItem := itemMap["value"]
					if vItem == nil {
						vItem = itemMap["Value"]
					}
					switch k {
					case "image":
						imageVal = vItem
					case "options":
						optionsVal = vItem
					case "env", "environment":
						envVal = vItem
					case "volumes":
						volumesVal = vItem
					case "ports":
						portsVal = vItem
					}
				}
			}
		} else if _, hasImage := val["image"]; hasImage || val["Image"] != nil {
			imageVal = val["image"]
			if imageVal == nil {
				imageVal = val["Image"]
			}
			optionsVal = val["options"]
			if optionsVal == nil {
				optionsVal = val["Options"]
			}
			envVal = val["environment"]
			if envVal == nil {
				envVal = val["Environment"]
			}
			if envVal == nil {
				envVal = val["env"]
			}
			if envVal == nil {
				envVal = val["Env"]
			}
			volumesVal = val["volumes"]
			if volumesVal == nil {
				volumesVal = val["Volumes"]
			}
			portsVal = val["ports"]
			if portsVal == nil {
				portsVal = val["Ports"]
			}
		} else {
			str := extractASTString(val)
			if str != "" {
				return &JobContainer{Image: str}
			}
			return nil
		}

		imageStr := extractASTString(imageVal)
		if imageStr == "" {
			return nil
		}
		env := extractASTMap(envVal)
		if len(env) == 0 {
			env = nil
		}
		return &JobContainer{
			Image:       imageStr,
			Options:     extractASTString(optionsVal),
			Environment: env,
			Volumes:     extractASTSlice(volumesVal),
			Ports:       extractASTSlice(portsVal),
		}
	default:
		str := extractASTString(v)
		if str != "" {
			return &JobContainer{Image: str}
		}
		return nil
	}
}

func findInContextOrMap(obj any, key string) any {
	if obj == nil {
		return nil
	}
	if m, ok := obj.(map[string]any); ok {
		for k, v := range m {
			if strings.EqualFold(k, key) {
				return v
			}
		}
		if mapSlice, ok := m["map"].([]any); ok {
			for _, item := range mapSlice {
				if itemMap, ok := item.(map[string]any); ok {
					k := extractASTString(itemMap["key"])
					if k == "" {
						k = extractASTString(itemMap["Key"])
					}
					if strings.EqualFold(k, key) {
						vItem := itemMap["value"]
						if vItem == nil {
							vItem = itemMap["Value"]
						}
						return vItem
					}
				}
			}
		}
	}
	return nil
}

// UnmarshalJSON unmarshals a JobSpecification from either direct format or GitHub Actions AgentJobRequestMessage format.
func (j *JobSpecification) UnmarshalJSON(data []byte) error {
	var raw rawJobSpec
	if err := json.Unmarshal(data, &raw); err != nil {
		return err
	}

	j.JobID = raw.JobID
	if j.JobID == "" {
		j.JobID = raw.JobIDAlt
	}
	if j.JobID == "" {
		j.JobID = raw.ID
	}
	j.ID = raw.ID
	if j.ID == "" {
		j.ID = j.JobID
	}

	j.EventName = raw.EventName
	j.IsLocalExecution = raw.IsLocalExecution

	jobDisplayName := extractASTString(raw.JobDisplayName)
	jobName := extractASTString(raw.JobName)
	if jobDisplayName != "" && (jobName == "" || jobName == "__default") {
		j.JobName = jobDisplayName
	} else if jobName != "" {
		j.JobName = jobName
	} else if jobDisplayName != "" {
		j.JobName = jobDisplayName
	} else {
		j.JobName = extractASTString(raw.Name)
	}

	j.PlanID = raw.PlanID
	if j.PlanID == "" && raw.Plan != nil {
		if raw.Plan.PlanID != "" {
			j.PlanID = raw.Plan.PlanID
		} else if raw.Plan.ID != "" {
			j.PlanID = raw.Plan.ID
		}
	}

	j.ContextData = raw.ContextData

	envVars := extractEnvironmentVariables(raw.EnvironmentVariables)
	if len(envVars) == 0 {
		envVars = extractEnvironmentVariables(raw.Environment)
	}
	if len(envVars) > 0 {
		j.Environment = envVars
	}

	vars := make(map[string]string)
	if len(raw.Variables) > 0 {
		for k, v := range raw.Variables {
			if v == nil {
				continue
			}
			vars[k] = extractASTString(v)
		}
	}
	if raw.Resources != nil && len(raw.Resources.Repositories) > 0 {
		for _, repo := range raw.Resources.Repositories {
			if repo.Name != "" && vars["GITHUB_REPOSITORY"] == "" {
				vars["GITHUB_REPOSITORY"] = repo.Name
			}
			if repo.Version != "" && vars["GITHUB_REF"] == "" {
				vars["GITHUB_REF"] = repo.Version
			}
		}
	}
	if len(vars) > 0 {
		j.Variables = vars
		if j.EventName == "" && vars["GITHUB_EVENT_NAME"] != "" {
			j.EventName = vars["GITHUB_EVENT_NAME"]
		}
	}

	if len(raw.Steps) > 0 {
		steps := make([]JobStep, 0, len(raw.Steps))
		for _, s := range raw.Steps {
			stepID := s.ID

			displayName := extractASTString(s.DisplayName)
			if displayName == "" {
				displayName = extractASTString(s.DisplayNameToken)
			}
			stepName := displayName
			if stepName == "" {
				stepName = extractASTString(s.Name)
			}
			if stepName == "" {
				stepName = s.ContextName
			}
			if stepName == "" {
				stepName = stepID
			}

			stepInputs := extractASTMap(s.Inputs)
			stepEnv := extractASTMap(s.Environment)

			stepType := s.Type
			if stepType == "" && s.Reference != nil && s.Reference.Type != "" {
				if s.Reference.Type == "repository" {
					stepType = "uses"
				} else {
					stepType = s.Reference.Type
				}
			}

			script := extractASTString(s.Script)
			if script == "" && stepInputs != nil {
				if v, ok := stepInputs["script"]; ok && v != "" {
					script = v
				} else if v, ok := stepInputs["run"]; ok && v != "" {
					script = v
				}
			}

			workDir := extractASTString(s.WorkingDirectory)
			if workDir == "" && stepInputs != nil {
				if v, ok := stepInputs["workingDirectory"]; ok && v != "" {
					workDir = v
				} else if v, ok := stepInputs["working-directory"]; ok && v != "" {
					workDir = v
				}
			}

			if stepType == "" || stepType == "repository" {
				if script != "" {
					stepType = "script"
				} else if s.Reference != nil && (strings.HasPrefix(s.Reference.Name, "actions/") || strings.Contains(s.Reference.Name, "@") || s.Reference.Type == "repository") {
					stepType = "uses"
				} else if stepInputs != nil && stepInputs["uses"] != "" {
					stepType = "uses"
				}
			}

			var ref *StepReference
			if s.Reference != nil && (s.Reference.Type == "repository" || (s.Reference.Type != "script" && s.Reference.Name != "")) {
				refVersion := s.Reference.Version
				refValue := s.Reference.Ref
				if refValue == "" && refVersion != "" {
					refValue = refVersion
				} else if refVersion == "" && refValue != "" {
					refVersion = refValue
				}
				ref = &StepReference{
					ID:      s.Reference.ID,
					Name:    s.Reference.Name,
					Version: refVersion,
					Ref:     refValue,
					Type:    s.Reference.Type,
					Path:    s.Reference.Path,
				}
			}

			condStr := extractConditionString(s.Condition)
			if condStr == "" {
				condStr = extractConditionString(s.ConditionAlt)
			}
			if condStr == "" {
				condStr = extractConditionString(s.If)
			}
			if condStr == "" {
				condStr = extractConditionString(s.IfAlt)
			}
			if condStr == "" {
				condStr = "success()"
			}

			steps = append(steps, JobStep{
				ID:               stepID,
				Name:             stepName,
				Type:             stepType,
				Script:           script,
				WorkingDirectory: workDir,
				Environment:      stepEnv,
				Inputs:           stepInputs,
				Reference:        ref,
				Condition:        condStr,
			})
		}
		j.Steps = steps
	}

	// Fallback plan ID from variables
	if j.PlanID == "" && len(j.Variables) > 0 {
		for _, varKey := range []string{"system.planId", "system.orchestrationId"} {
			if pid, ok := j.Variables[varKey]; ok && pid != "" {
				j.PlanID = pid
				break
			}
		}
	}

	// Extract Endpoints and JobToken from top-level Endpoints and Resources.Endpoints
	seenEndpoints := make(map[string]bool)
	appendRawEndpoints := func(endpoints []rawEndpoint) {
		for _, ep := range endpoints {
			key := ep.Name + "|" + ep.URL
			if seenEndpoints[key] {
				continue
			}
			seenEndpoints[key] = true
			dataMap := make(map[string]string)
			for k, v := range ep.Data {
				if v != nil {
					dataMap[k] = extractASTString(v)
				}
			}
			j.Endpoints = append(j.Endpoints, ServiceEndpoint{
				Name:          ep.Name,
				Type:          ep.Type,
				URL:           ep.URL,
				Authorization: ep.Authorization,
				Data:          dataMap,
			})

			if j.JobToken == "" && ep.Authorization != nil && ep.Authorization.Parameters != nil {
				for _, paramKey := range []string{"AccessToken", "accesstoken", "Token", "token"} {
					if tok, ok := ep.Authorization.Parameters[paramKey]; ok && tok != "" {
						j.JobToken = tok
						break
					}
				}
			}
		}
	}

	if len(raw.Endpoints) > 0 {
		appendRawEndpoints(raw.Endpoints)
	}
	if raw.Resources != nil && len(raw.Resources.Endpoints) > 0 {
		appendRawEndpoints(raw.Resources.Endpoints)
	}

	if j.JobToken == "" && len(j.Variables) > 0 {
		for _, varKey := range []string{"system.accessToken", "ACTIONS_RUNTIME_TOKEN", "ACTIONS_ID_TOKEN_REQUEST_TOKEN"} {
			if tok, ok := j.Variables[varKey]; ok && tok != "" {
				j.JobToken = tok
				break
			}
		}
	}

	// Extract container configuration
	if raw.Resources != nil && len(raw.Resources.Containers) > 0 {
		var target *rawContainer
		for i := range raw.Resources.Containers {
			if strings.EqualFold(raw.Resources.Containers[i].Alias, "container") {
				target = &raw.Resources.Containers[i]
				break
			}
		}
		if target == nil {
			target = &raw.Resources.Containers[0]
		}
		j.Container = parseRawContainer(target)
	}

	if j.Container == nil && raw.Container != nil {
		j.Container = parseContainerFromAny(raw.Container)
	}

	if j.Container == nil && raw.ContextData != nil {
		jobVal := findInContextOrMap(raw.ContextData, "job")
		if jobVal != nil {
			containerVal := findInContextOrMap(jobVal, "container")
			if containerVal != nil {
				j.Container = parseContainerFromAny(containerVal)
			}
		}
	}

	return nil
}

// TaskResult enum values matching GitHub Actions / Azure Pipelines TaskResult enum.
const (
	TaskResultSucceeded           = "succeeded"
	TaskResultSucceededWithIssues = "succeededWithIssues"
	TaskResultFailed              = "failed"
	TaskResultCanceled            = "canceled"
	TaskResultSkipped             = "skipped"
	TaskResultAbandoned           = "abandoned"
)

// NormalizeConclusion maps conclusion strings to GitHub Actions TaskResult values.
func NormalizeConclusion(conclusion string) string {
	switch strings.ToLower(strings.TrimSpace(conclusion)) {
	case "success", "succeeded":
		return TaskResultSucceeded
	case "failure", "failed":
		return TaskResultFailed
	case "cancel", "canceled", "cancelled":
		return TaskResultCanceled
	case "skipped":
		return TaskResultSkipped
	case "abandoned":
		return TaskResultAbandoned
	case "succeededwithissues":
		return TaskResultSucceededWithIssues
	default:
		return conclusion
	}
}

// StepResult represents the execution outcome of an individual job step.
type StepResult struct {
	ID         string `json:"id"`
	ExternalID string `json:"external_id,omitempty"`
	Conclusion string `json:"conclusion"` // "succeeded", "failed", "skipped"
	ExitCode   int    `json:"exitCode"`
}

// CompleteJobRequest represents the final job execution status sent to the Run Service.
type CompleteJobRequest struct {
	PlanID      string            `json:"planId"`
	JobID       string            `json:"jobId"`
	Conclusion  string            `json:"conclusion"` // "succeeded", "failed", "canceled"
	Outputs     map[string]string `json:"outputs,omitempty"`
	StepResults []StepResult      `json:"stepResults,omitempty"`
}

func (r *CompleteJobRequest) MarshalJSON() ([]byte, error) {
	type wireOutputVal struct {
		Value    string `json:"value"`
		IsSecret bool   `json:"isSecret,omitempty"`
	}
	var wireOutputs map[string]wireOutputVal
	if len(r.Outputs) > 0 {
		wireOutputs = make(map[string]wireOutputVal, len(r.Outputs))
		for k, v := range r.Outputs {
			wireOutputs[k] = wireOutputVal{Value: v}
		}
	}
	type wireStepResult struct {
		ExternalID string `json:"external_id,omitempty"`
		ID         string `json:"id,omitempty"`
		Conclusion string `json:"conclusion,omitempty"`
		ExitCode   int    `json:"exitCode,omitempty"`
	}
	var wireSteps []wireStepResult
	if len(r.StepResults) > 0 {
		wireSteps = make([]wireStepResult, len(r.StepResults))
		for i, s := range r.StepResults {
			extID := s.ExternalID
			if extID == "" {
				extID = s.ID
			}
			wireSteps[i] = wireStepResult{
				ExternalID: extID,
				ID:         s.ID,
				Conclusion: NormalizeConclusion(s.Conclusion),
				ExitCode:   s.ExitCode,
			}
		}
	}
	return json.Marshal(struct {
		PlanID      string                   `json:"planId"`
		JobID       string                   `json:"jobId"`
		Conclusion  string                   `json:"conclusion"`
		Outputs     map[string]wireOutputVal `json:"outputs,omitempty"`
		StepResults []wireStepResult         `json:"stepResults,omitempty"`
	}{
		PlanID:      r.PlanID,
		JobID:       r.JobID,
		Conclusion:  NormalizeConclusion(r.Conclusion),
		Outputs:     wireOutputs,
		StepResults: wireSteps,
	})
}

func (r *CompleteJobRequest) UnmarshalJSON(data []byte) error {
	var raw struct {
		PlanID      string         `json:"planId"`
		JobID       string         `json:"jobId"`
		Conclusion  string         `json:"conclusion"`
		Outputs     map[string]any `json:"outputs"`
		StepResults []StepResult   `json:"stepResults"`
	}
	if err := json.Unmarshal(data, &raw); err != nil {
		return err
	}
	r.PlanID = raw.PlanID
	r.JobID = raw.JobID
	r.Conclusion = raw.Conclusion
	r.StepResults = raw.StepResults
	if len(raw.Outputs) > 0 {
		r.Outputs = make(map[string]string, len(raw.Outputs))
		for k, v := range raw.Outputs {
			if v == nil {
				continue
			}
			switch val := v.(type) {
			case string:
				r.Outputs[k] = val
			case map[string]any:
				if innerVal, ok := val["value"]; ok && innerVal != nil {
					r.Outputs[k] = fmt.Sprint(innerVal)
				}
			default:
				r.Outputs[k] = fmt.Sprint(val)
			}
		}
	}
	return nil
}

// RunServiceClient interacts with the GitHub Actions Run Service endpoints.
type RunServiceClient struct {
	client *http.Client
}

// NewRunServiceClient creates a new RunServiceClient with the provided http.Client.
// If client is nil, a default http.Client with hardened timeouts and TLS defaults is used.
func NewRunServiceClient(client *http.Client) *RunServiceClient {
	if client == nil {
		client = newDefaultHTTPClient(30 * time.Second)
	}
	return &RunServiceClient{client: client}
}

// AcquireJob requests and claims a job from the Run Service using the runnerRequestID.
func (c *RunServiceClient) AcquireJob(ctx context.Context, runServiceURL, runnerRequestID, billingOwnerID, token string) (*JobSpecification, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	cleanURL := strings.TrimRight(strings.TrimSpace(runServiceURL), "/")
	if cleanURL == "" {
		return nil, errors.New("empty run service URL")
	}
	if _, err := validateRemoteURL(cleanURL); err != nil {
		return nil, fmt.Errorf("invalid run service URL: %w", err)
	}
	if strings.TrimSpace(runnerRequestID) == "" {
		return nil, errors.New("empty runner request ID")
	}

	reqURL, err := url.Parse(cleanURL + "/acquirejob")
	if err != nil {
		return nil, fmt.Errorf("parse run service URL: %w", err)
	}

	reqPayload := struct {
		JobMessageID   string `json:"jobMessageId"`
		BillingOwnerID string `json:"billingOwnerId"`
	}{
		JobMessageID:   runnerRequestID,
		BillingOwnerID: billingOwnerID,
	}

	jsonBytes, err := json.Marshal(reqPayload)
	if err != nil {
		return nil, fmt.Errorf("marshal acquire payload: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, reqURL.String(), bytes.NewReader(jsonBytes))
	if err != nil {
		return nil, fmt.Errorf("create acquire request: %w", err)
	}

	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")

	resp, err := c.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("execute acquire request: %w", err)
	}
	defer drainAndClose(resp.Body)

	bodyBytes, err := io.ReadAll(io.LimitReader(resp.Body, 10<<20))
	if err != nil {
		return nil, fmt.Errorf("read acquire response: %w", err)
	}

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("acquire job returned status %d: %s", resp.StatusCode, string(bodyBytes))
	}

	return ParseAcquiredJob(bodyBytes)
}

// ParseAcquiredJob parses raw acquire job JSON payload into a JobSpecification.
func ParseAcquiredJob(data []byte) (*JobSpecification, error) {
	var jobSpec JobSpecification
	if err := json.Unmarshal(data, &jobSpec); err != nil {
		return nil, fmt.Errorf("unmarshal job specification: %w", err)
	}
	return &jobSpec, nil
}

// RenewJob renews the lock on an active job with the Run Service.
// Returns the time until which the job is locked.
func (c *RunServiceClient) RenewJob(ctx context.Context, runServiceURL, planID, jobID, token string) (time.Time, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	cleanURL := strings.TrimRight(strings.TrimSpace(runServiceURL), "/")
	if cleanURL == "" {
		return time.Time{}, errors.New("empty run service URL")
	}
	if _, err := validateRemoteURL(cleanURL); err != nil {
		return time.Time{}, fmt.Errorf("invalid run service URL: %w", err)
	}
	if strings.TrimSpace(planID) == "" {
		return time.Time{}, errors.New("empty plan ID")
	}
	if strings.TrimSpace(jobID) == "" {
		return time.Time{}, errors.New("empty job ID")
	}

	reqURL, err := url.Parse(cleanURL + "/renewjob")
	if err != nil {
		return time.Time{}, fmt.Errorf("parse run service URL: %w", err)
	}

	reqPayload := struct {
		PlanID string `json:"planId"`
		JobID  string `json:"jobId"`
	}{
		PlanID: planID,
		JobID:  jobID,
	}

	jsonBytes, err := json.Marshal(reqPayload)
	if err != nil {
		return time.Time{}, fmt.Errorf("marshal renew payload: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, reqURL.String(), bytes.NewReader(jsonBytes))
	if err != nil {
		return time.Time{}, fmt.Errorf("create renew request: %w", err)
	}

	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")

	resp, err := c.client.Do(req)
	if err != nil {
		return time.Time{}, fmt.Errorf("execute renew request: %w", err)
	}
	defer drainAndClose(resp.Body)

	bodyBytes, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return time.Time{}, fmt.Errorf("read renew response: %w", err)
	}

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return time.Time{}, fmt.Errorf("renew job returned status %d: %s", resp.StatusCode, string(bodyBytes))
	}

	var renewResp struct {
		LockedUntil    any `json:"lockedUntil"`
		LockedUntilAlt any `json:"LockedUntil"`
	}
	if err := json.Unmarshal(bodyBytes, &renewResp); err != nil {
		return time.Time{}, fmt.Errorf("unmarshal renew response: %w", err)
	}

	val := renewResp.LockedUntil
	if val == nil {
		val = renewResp.LockedUntilAlt
	}
	if val == nil {
		return time.Time{}, errors.New("missing lockedUntil in renew response")
	}

	return parseTimeValue(val)
}

func parseTimeValue(val any) (time.Time, error) {
	switch v := val.(type) {
	case string:
		formats := []string{
			time.RFC3339Nano,
			time.RFC3339,
			"2006-01-02T15:04:05Z07:00",
			"2006-01-02T15:04:05Z",
			"2006-01-02T15:04:05",
			time.RFC1123,
			time.RFC1123Z,
		}
		for _, layout := range formats {
			if t, err := time.Parse(layout, v); err == nil {
				return t, nil
			}
		}
		return time.Time{}, fmt.Errorf("cannot parse lockedUntil timestamp string %q", v)
	case float64:
		intVal := int64(v)
		if intVal > 1e11 {
			return time.UnixMilli(intVal).UTC(), nil
		}
		return time.Unix(intVal, 0).UTC(), nil
	case int64:
		if v > 1e11 {
			return time.UnixMilli(v).UTC(), nil
		}
		return time.Unix(v, 0).UTC(), nil
	case json.Number:
		if i, err := v.Int64(); err == nil {
			if i > 1e11 {
				return time.UnixMilli(i).UTC(), nil
			}
			return time.Unix(i, 0).UTC(), nil
		}
		if f, err := v.Float64(); err == nil {
			i := int64(f)
			if i > 1e11 {
				return time.UnixMilli(i).UTC(), nil
			}
			return time.Unix(i, 0).UTC(), nil
		}
	}
	return time.Time{}, fmt.Errorf("unsupported lockedUntil value type: %T", val)
}

// CompleteJob reports job execution results and completion status to the Run Service.
func (c *RunServiceClient) CompleteJob(ctx context.Context, runServiceURL, token string, req *CompleteJobRequest) error {
	if ctx == nil {
		ctx = context.Background()
	}
	cleanURL := strings.TrimRight(strings.TrimSpace(runServiceURL), "/")
	if cleanURL == "" {
		return errors.New("empty run service URL")
	}
	if _, err := validateRemoteURL(cleanURL); err != nil {
		return fmt.Errorf("invalid run service URL: %w", err)
	}
	if req == nil {
		return errors.New("nil complete job request")
	}
	if strings.TrimSpace(req.PlanID) == "" {
		return errors.New("empty plan ID")
	}
	if strings.TrimSpace(req.JobID) == "" {
		return errors.New("empty job ID")
	}
	if strings.TrimSpace(req.Conclusion) == "" {
		return errors.New("empty conclusion")
	}

	reqURL, err := url.Parse(cleanURL + "/completejob")
	if err != nil {
		return fmt.Errorf("parse run service URL: %w", err)
	}

	jsonBytes, err := json.Marshal(req)
	if err != nil {
		return fmt.Errorf("marshal complete payload: %w", err)
	}

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, reqURL.String(), bytes.NewReader(jsonBytes))
	if err != nil {
		return fmt.Errorf("create complete request: %w", err)
	}

	if token != "" {
		httpReq.Header.Set("Authorization", "Bearer "+token)
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Accept", "application/json")

	resp, err := c.client.Do(httpReq)
	if err != nil {
		return fmt.Errorf("execute complete request: %w", err)
	}
	defer drainAndClose(resp.Body)

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		bodyBytes, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
		return fmt.Errorf("complete job returned status %d: %s", resp.StatusCode, string(bodyBytes))
	}

	return nil
}
