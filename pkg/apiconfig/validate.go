package apiconfig

import (
	"encoding/json"
	"fmt"
	"regexp"
	"strconv"
	"strings"
)

// pathTraversalPattern detects path traversal attempts.
var pathTraversalPattern = regexp.MustCompile(`\.\.[\\/]|[\\/]\.\.`)

// queryInjectionPattern detects query string injection in path params.
var queryInjectionPattern = regexp.MustCompile(`[?&#]`)

// validatePathParam validates a single path parameter value against its config.
func validatePathParam(param Param, value string) (err error) {
	if value == "" {
		if param.Required {
			err = fmt.Errorf("required parameter %q is empty", param.Name)
			return err
		}
		return err
	}

	// Always block path traversal
	if pathTraversalPattern.MatchString(value) {
		err = fmt.Errorf("parameter %q contains path traversal", param.Name)
		return err
	}

	// Always block query injection in path params
	if param.In == "path" && queryInjectionPattern.MatchString(value) {
		err = fmt.Errorf("parameter %q contains query injection characters", param.Name)
		return err
	}

	// Validate against custom regex if provided
	if param.Validate != "" {
		matched, matchErr := regexp.MatchString("^(?:"+param.Validate+")$", value)
		if matchErr != nil {
			err = fmt.Errorf("invalid validation pattern for %q: %w", param.Name, matchErr)
			return err
		}

		if !matched {
			err = fmt.Errorf("parameter %q value %q does not match pattern %q", param.Name, value, param.Validate)
			return err
		}
	}

	return err
}

// validateAndBuildURL validates all parameters and builds the final request URL
// and (for non-GET endpoints) the JSON request body. Params are routed by their
// `in`: `path` values are substituted into the path, `query` values into the
// query string, and `body` values collected into a JSON object returned as body.
// A nil body means no body params were supplied.
func validateAndBuildURL(baseURL string, endpoint Endpoint, args map[string]interface{}) (requestURL string, queryParams map[string]string, body []byte, err error) {
	queryParams = make(map[string]string)

	var path string
	path, err = buildPathAndQuery(endpoint, args, queryParams)
	if err != nil {
		return requestURL, queryParams, body, err
	}

	// Verify no unresolved path placeholders remain
	if strings.Contains(path, "{") {
		err = fmt.Errorf("unresolved path placeholders in %q", path)
		return requestURL, queryParams, body, err
	}

	requestURL = strings.TrimRight(baseURL, "/") + path

	body, err = collectBodyParams(endpoint, args)
	return requestURL, queryParams, body, err
}

// buildPathAndQuery substitutes `in: path` params into the path and fills
// queryParams from `in: query` params (the default). `in: body` params are
// skipped here — collectBodyParams handles them.
func buildPathAndQuery(endpoint Endpoint, args map[string]interface{}, queryParams map[string]string) (path string, err error) {
	path = endpoint.Path

	for _, param := range endpoint.Params {
		if param.In == "body" {
			continue
		}

		value := extractStringArg(args, param.Name)

		err = validatePathParam(param, value)
		if err != nil {
			return path, err
		}

		if value == "" {
			continue
		}

		if param.In == "path" {
			placeholder := "{" + param.Name + "}"
			if !strings.Contains(path, placeholder) {
				err = fmt.Errorf("path parameter %q placeholder not found in path %q", param.Name, endpoint.Path)
				return path, err
			}
			path = strings.ReplaceAll(path, placeholder, value)
		} else {
			queryParams[param.Name] = value
		}
	}

	return path, err
}

// collectBodyParams validates and gathers `in: body` params into a JSON object.
// Returns a nil body when the endpoint has no body params.
func collectBodyParams(endpoint Endpoint, args map[string]interface{}) (body []byte, err error) {
	bodyParams := make(map[string]interface{})

	for _, param := range endpoint.Params {
		if param.In != "body" {
			continue
		}

		err = validateBodyParam(param, args)
		if err != nil {
			return body, err
		}

		if raw, ok := args[param.Name]; ok && raw != nil {
			bodyParams[param.Name] = raw
		}
	}

	if len(bodyParams) == 0 {
		return body, err
	}

	body, err = json.Marshal(bodyParams)
	if err != nil {
		err = fmt.Errorf("marshaling request body: %w", err)
	}

	return body, err
}

// validateBodyParam checks a `in: body` parameter: presence when required, and
// the optional per-param `validate` regex against its string form. It does NOT
// apply the path-traversal or query-injection checks that path/query params get
// — those guard URL construction, not a JSON body.
func validateBodyParam(param Param, args map[string]interface{}) (err error) {
	raw, present := args[param.Name]
	if !present || raw == nil {
		if param.Required {
			err = fmt.Errorf("required parameter %q is missing", param.Name)
		}
		return err
	}

	if param.Validate == "" {
		return err
	}

	value := extractStringArg(args, param.Name)

	matched, matchErr := regexp.MatchString("^(?:"+param.Validate+")$", value)
	if matchErr != nil {
		err = fmt.Errorf("invalid validation pattern for %q: %w", param.Name, matchErr)
		return err
	}

	if !matched {
		err = fmt.Errorf("parameter %q value %q does not match pattern %q", param.Name, value, param.Validate)
	}

	return err
}

func extractStringArg(args map[string]interface{}, key string) (value string) {
	raw, ok := args[key]
	if !ok {
		return value
	}

	switch v := raw.(type) {
	case string:
		value = v
	case float64:
		value = fmt.Sprintf("%v", v)
	case bool:
		value = strconv.FormatBool(v)
	}

	return value
}
