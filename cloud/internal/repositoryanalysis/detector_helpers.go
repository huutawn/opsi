package repositoryanalysis

import (
	"path"
	"sort"
	"strconv"
	"strings"
	"time"
)

func analysisCandidate(value string) bool {
	base := strings.ToLower(path.Base(value))
	return strings.HasSuffix(base, ".json") || strings.HasSuffix(base, ".cs") || strings.HasSuffix(base, ".ts") || strings.HasSuffix(base, ".tsx") || strings.HasSuffix(base, ".js") || strings.HasSuffix(base, ".md") || strings.HasSuffix(base, ".env.example") || base == "readme"
}

func underRoot(value, root string) bool {
	return root == "." || value == root || strings.HasPrefix(value, root+"/")
}

func imageResourceType(image string) string {
	value := strings.ToLower(image)
	switch {
	case strings.Contains(value, "postgres"):
		return "postgres"
	case strings.Contains(value, "valkey") || strings.Contains(value, "redis"):
		return "redis"
	case strings.Contains(value, "kafka"):
		return "kafka"
	default:
		return ""
	}
}

func displayResource(kind string) string {
	switch kind {
	case "redis":
		return "Valkey"
	case "postgres":
		return "PostgreSQL"
	default:
		return strings.ToUpper(kind[:1]) + kind[1:]
	}
}

func firstComposePort(values []string) int {
	for _, value := range values {
		last := value
		if index := strings.LastIndex(value, ":"); index >= 0 {
			last = value[index+1:]
		}
		last = strings.TrimSuffix(last, "/tcp")
		port, _ := strconv.Atoi(last)
		if port > 0 && port <= 65535 {
			return port
		}
	}
	return 0
}

func firstExisting(files map[string]File, values ...string) string {
	for _, value := range values {
		if _, ok := files[value]; ok {
			return value
		}
	}
	return ""
}

func cleanRoot(value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return "."
	}
	return path.Clean(value)
}

func safePath(value string, allowDot bool) bool {
	if value == "." {
		return allowDot
	}
	return value != "" && !strings.HasPrefix(value, "/") && !strings.Contains(value, "\\") && path.Clean(value) == value && !strings.Contains(value, "../")
}

func validKey(value string) bool {
	if len(value) < 1 || len(value) > 63 {
		return false
	}
	for index, character := range value {
		if !(character >= 'a' && character <= 'z' || character >= '0' && character <= '9' || (index > 0 && (character == '-' || character == '_'))) {
			return false
		}
	}
	return value[len(value)-1] != '-' && value[len(value)-1] != '_'
}

func slug(value string) string {
	value = strings.ToLower(value)
	var result strings.Builder
	for _, character := range value {
		if character >= 'a' && character <= 'z' || character >= '0' && character <= '9' {
			result.WriteRune(character)
		} else if result.Len() > 0 && result.String()[result.Len()-1] != '-' {
			result.WriteByte('-')
		}
	}
	return strings.Trim(result.String(), "-")
}

func sortResult(result *Result) {
	sort.Slice(result.Applications, func(i, j int) bool { return result.Applications[i].Key < result.Applications[j].Key })
	sort.Slice(result.Resources, func(i, j int) bool { return result.Resources[i].LogicalName < result.Resources[j].LogicalName })
	for index := range result.Dependencies {
		sort.Slice(result.Dependencies[index].Injections, func(i, j int) bool {
			return result.Dependencies[index].Injections[i].EnvironmentName < result.Dependencies[index].Injections[j].EnvironmentName
		})
	}
	sort.Slice(result.Dependencies, func(i, j int) bool {
		if result.Dependencies[i].From == result.Dependencies[j].From {
			return result.Dependencies[i].To+"\x00"+result.Dependencies[i].Path < result.Dependencies[j].To+"\x00"+result.Dependencies[j].Path
		}
		return result.Dependencies[i].From < result.Dependencies[j].From
	})
	sort.Slice(result.Bindings, func(i, j int) bool {
		return result.Bindings[i].From+"\x00"+result.Bindings[i].To+"\x00"+result.Bindings[i].Path < result.Bindings[j].From+"\x00"+result.Bindings[j].To+"\x00"+result.Bindings[j].Path
	})
	sort.Slice(result.Secrets, func(i, j int) bool {
		return result.Secrets[i].ApplicationKey+"\x00"+result.Secrets[i].Name < result.Secrets[j].ApplicationKey+"\x00"+result.Secrets[j].Name
	})
	sort.SliceStable(result.Issues, func(i, j int) bool {
		return result.Issues[i].Code+"\x00"+result.Issues[i].Path+"\x00"+result.Issues[i].Message < result.Issues[j].Code+"\x00"+result.Issues[j].Path+"\x00"+result.Issues[j].Message
	})
}

func (d Detector) clock() time.Time {
	if d.Now != nil {
		return d.Now().UTC()
	}
	return time.Now().UTC()
}
