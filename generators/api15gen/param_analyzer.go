package main

import (
	"fmt"
	"regexp"
	"sort"
	"strings"

	"bitbucket.org/pkg/inflect"
)

var (
	// Capture root of path
	rootRegexp = regexp.MustCompile("([^\\[]+)\\[")

	// Parent path regular expression
	parentPathRegexp = regexp.MustCompile(`^(.*)\[.+\]$`)

	// Child path regular expression
	childPathRegexp = regexp.MustCompile(`^.*\[(.+)\]$`)

	// Capture all alphanumerical parts to build go identifier from raw param name
	partsRegexp = regexp.MustCompile("[^[:alnum:]]+")
)

// Analyzer exposes "analyze" method which initialized all the fields but 'rawParams' which is
// initialized by factory method.
// The analyzer takes a map describing the parameters of a method as found in the API JSON and
// produces the corresponding set of ActionParam structs.
type ParamAnalyzer struct {
	// Raw parameter hashes as found in JSON
	rawParams map[string]interface{}

	/* Fields below are computed by 'analyze' */

	// Parameter types indexed by name
	ParamTypes map[string]*ObjectDataType
	// Query parameters sorted alphabetically by name (appear in query string)
	QueryParams []*ActionParam
	// Payload parameters sorted alphabetically by name (used in request body)
	PayloadParams []*ActionParam
}

// Factory method, initialize 'path' and 'rawParams' fields
func NewAnalyzer(params map[string]interface{}) *ParamAnalyzer {
	return &ParamAnalyzer{rawParams: params}
}

// Analyze all parameters and categorize them
// Initialize all fields of ParamAnalyzer struct
func (p *ParamAnalyzer) Analyze() {
	// Order params using their length so "foo[bar]" is analyzed before "foo"
	params := p.rawParams
	paths := make([]string, len(params))
	i := 0
	for n, _ := range params {
		paths[i] = n
		i += 1
	}
	sort.Strings(paths)
	sort.Sort(ByReverseLength(paths))

	// Iterate through all params and build corresponding ActionParam structs
	parsed := map[string]*ActionParam{}
	top := map[string]*ActionParam{}
	for _, path := range paths {
		if strings.HasSuffix(path, "[*]") {
			// Cheat a little bit - there a couple of cases where parent type is
			// Hash instead of Enumerable, make that enumerable everywhere
			// There are also cases where there's no parent path, fix that up also
			matches := parentPathRegexp.FindStringSubmatch(path)
			if hashParam, ok := params[matches[1]].(map[string]interface{}); ok {
				hashParam["class"] = "Enumerable"
			} else {
				// Create parent
				rawParams := map[string]interface{}{}
				parentPath := matches[1]
				parsed[parentPath] = p.newParam(parentPath, rawParams,
					new(EnumerableDataType))
				if parentPathRegexp.FindStringSubmatch(parentPath) == nil {
					top[parentPath] = parsed[parentPath]
				}
			}
			continue
		}
		var child *ActionParam
		origPath := path
		origParam := params[path].(map[string]interface{})
		matches := parentPathRegexp.FindStringSubmatch(path)
		isTop := (matches == nil)
		if prev, ok := parsed[path]; ok {
			if isTop {
				top[path] = prev
			}
			continue
		}
		for matches != nil {
			param := params[path].(map[string]interface{})
			parentPath := matches[1]
			var isArrayChild bool
			if strings.HasSuffix(parentPath, "[]") {
				isArrayChild = true
			}
			if parent, ok := parsed[parentPath]; ok {
				a, ok := parent.Type.(*ArrayDataType)
				if ok {
					parent = a.ElemType
				}
				child = p.parseParam(path, param, child)
				if _, ok = parent.Type.(*EnumerableDataType); !ok {
					o := parent.Type.(*ObjectDataType)
					o.Fields = appendSorted(o.Fields, child)
					parsed[path] = child
				}
				break // No need to keep going back, we already have a parent
			} else {
				child = p.parseParam(path, param, child)
				parsed[path] = child
				if isArrayChild {
					// Generate array item as it's not listed explicitly in JSON
					itemPath := nativeNameFromPath(matches[1]) + "[item]"
					typeName := p.typeName(matches[1])
					parent = p.newParam(itemPath, map[string]interface{}{},
						&ObjectDataType{typeName, []*ActionParam{child}})
					parsed[parentPath] = parent
					child = parent
					parentPath = parentPath[:len(parentPath)-2]
				}
			}
			path = parentPath
			matches = parentPathRegexp.FindStringSubmatch(path)
		}
		if isTop {
			if _, ok := parsed[path]; !ok {
				actionParam := p.parseParam(path, origParam, nil)
				parsed[path] = actionParam
			}
			top[path] = parsed[path]
		} else {
			matches := rootRegexp.FindStringSubmatch(origPath)
			rootPath := matches[1]
			if _, ok := parsed[rootPath]; !ok {
				parsed[rootPath] = p.parseParam(rootPath,
					params[rootPath].(map[string]interface{}), child)
			}
		}
	}

	// Now do a second pass on parsed params to generate their declarations
	p.ParamTypes = make(map[string]*ObjectDataType)
	p.Params = make(map[string]*ActionParam, len(top))
	for _, param := range top {
		p.recordTypes(param.Type)
		p.Params[param.Name] = param
	}

	allParams := make([]*ActionParam, len(p.Params))
	idx := 0
	for n, param := range p.Params {
		allParams[idx] = param
		idx += 1
	}
	sort.Sort(ByName(allParams))
	queryParams := []*ActionParam{}
	payloadParams := []*ActionParam{}
	for _, param := range allParams {
		pname := param.Name
		if isQueryParam(pname) {
			queryParams = append(queryParams, param)
		} else {
			payloadParams = append(payloadParams, param)
		}
	}
	p.QueryParams = queryParams
	p.PayloadParams = payloadParams
}

// Sort array of string by length
type ByReverseLength []string

func (s ByReverseLength) Len() int {
	return len(s)
}
func (s ByReverseLength) Swap(i, j int) {
	s[i], s[j] = s[j], s[i]
}
func (s ByReverseLength) Less(i, j int) bool {
	return len(s[i]) > len(s[j])
}

// Heuristic to determine whether given param is a query string param
// For now only consider view and filter...
func isQueryParam(n string) bool {
	return n == "view" || n == "filter"
}

// Recursively record all type declarations
func (p *ParamAnalyzer) recordTypes(root DataType) {
	if o, ok := root.(*ObjectDataType); ok {
		if _, found := p.ParamTypes[o.Name]; !found {
			p.ParamTypes[o.Name] = o
			for _, f := range o.Fields {
				p.recordTypes(f.Type)
			}
		}
	} else if a, ok := root.(*ArrayDataType); ok {
		p.recordTypes(a.ElemType.Type)
	}
}

// Sort action params by name
func appendSorted(params []*ActionParam, param *ActionParam) []*ActionParam {
	params = append(params, param)
	sort.Sort(ByName(params))
	return params
}

// Parse data type in context
func (p *ParamAnalyzer) parseDataType(path string, child *ActionParam) DataType {
	param := p.rawParams[path].(map[string]interface{})
	class := "String"
	if c, ok := param["class"].(string); ok {
		class = c
	}
	var res DataType
	switch class {
	case "Integer":
		i := BasicDataType("int")
		res = &i
	case "String":
		s := BasicDataType("string")
		res = &s
	case "Array":
		if child != nil {
			res = &ArrayDataType{child}
		} else {
			s := BasicDataType("string")
			p := p.newParam(fmt.Sprintf("%s[item]", path),
				map[string]interface{}{}, &s)
			res = &ArrayDataType{p}
		}
	case "Enumerable":
		res = new(EnumerableDataType)
	case "Hash":
		if current, ok := p.Params[path]; ok {
			res = current.Type
			o := res.(*ObjectDataType)
			o.Fields = appendSorted(o.Fields, child)
		} else {
			oname := p.typeName(path)
			res = &ObjectDataType{oname, []*ActionParam{child}}
		}
	}
	return res
}

func (p *ParamAnalyzer) typeName(path string) string {
	matches := childPathRegexp.FindStringSubmatch(path)
	res := path
	if matches != nil {
		res = matches[1]
	}
	return strings.Title(parseParamName(res))
}

// Build action param struct from json data
func (p *ParamAnalyzer) parseParam(path string, param map[string]interface{}, child *ActionParam) *ActionParam {
	dType := p.parseDataType(path, child)
	return p.newParam(path, param, dType)
}

// New parameter from raw values
func (p *ParamAnalyzer) newParam(path string, param map[string]interface{}, dType DataType) *ActionParam {
	var description, regexp string
	var mandatory, nonBlank bool
	var validValues []interface{}
	if d, ok := param["description"]; ok {
		description = d.(string)
	}
	if m, ok := param["mandatory"]; ok {
		mandatory = m.(bool)
	}
	if n, ok := param["non_blank"]; ok {
		nonBlank = n.(bool)
	}
	if r, ok := param["regexp"]; ok {
		regexp = r.(string)
	}
	if v, ok := param["valid_values"]; ok {
		validValues = v.([]interface{})
	}
	native := nativeNameFromPath(path)
	return &ActionParam{
		Name:        native,
		Description: removeBlankLines(description),
		VarName:     parseParamName(native),
		Type:        dType,
		Mandatory:   mandatory,
		NonBlank:    nonBlank,
		Regexp:      regexp,
		ValidValues: validValues,
	}
}

// Check whether string only contains blank characters
var blankRegexp = regexp.MustCompile(`^\s*$`)

// Helper method that removes blank lines from strings
func removeBlankLines(doc string) string {
	lines := strings.Split(doc, "\n")
	fullLines := make([]string, len(lines))
	i := 0
	for _, line := range lines {
		if len(line) > 0 && !blankRegexp.MatchString(line) {
			fullLines[i] = line
			i += 1
		}
	}
	return strings.Join(fullLines[:i], "\n")
}

// Extract name (leaf) from path
func nativeNameFromPath(path string) string {
	native := path
	matches := childPathRegexp.FindStringSubmatch(path)
	if matches != nil {
		native = matches[1]
	}
	return native
}

// Parse native names into go parameter names
func parseParamName(name string) string {
	if name == "r_s_version" {
		return "rsVersion"
	}
	if name == "type" {
		return "type_"
	}
	p := partsRegexp.ReplaceAllString(name, "_")
	return inflect.CamelizeDownFirst(p)
}
