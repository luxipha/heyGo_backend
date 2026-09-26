package env

import (
	"os"
	"strconv"
	"strings"
	"time"
)

// GetString retrieves the value of the environment variable named by the key.
// If the variable is empty or not present, it returns the specified default value.
func GetString(key, defaultValue string) string {
	val := os.Getenv(key)
	if val == "" {
		return defaultValue
	}
	return val
}

// ListenAddr returns an address suitable for a service's inbound listener.
// Cloud Run injects PORT and requires the ingress container to listen on it.
// Outside Cloud Run, the service-specific address and legacy default remain in
// effect so local and Kubernetes deployments keep their existing ports.
func ListenAddr(key, defaultValue string) string {
	if port := strings.TrimSpace(os.Getenv("PORT")); port != "" {
		return ":" + port
	}
	return GetString(key, defaultValue)
}

func GetCSV(key string, defaultValues []string) []string {
	value := strings.TrimSpace(os.Getenv(key))
	if value == "" {
		return append([]string(nil), defaultValues...)
	}
	parts := strings.Split(value, ",")
	result := make([]string, 0, len(parts))
	for _, part := range parts {
		if part = strings.TrimSpace(part); part != "" {
			result = append(result, part)
		}
	}
	if len(result) == 0 {
		return append([]string(nil), defaultValues...)
	}
	return result
}

// GetInt retrieves the value of the environment variable named by the key and converts it to an integer.
// If the variable is empty, not present, or cannot be converted to an integer, it returns the specified default value.
func GetInt(key string, defaultValue int) int {
	val := os.Getenv(key)
	if val == "" {
		return defaultValue
	}
	intVal, err := strconv.Atoi(val)
	if err != nil {
		return defaultValue
	}
	return intVal
}

// GetBool retrieves the value of the environment variable named by the key and converts it to a boolean.
// If the variable is empty, not present, or cannot be converted to a boolean, it returns the specified default value.
func GetBool(key string, defaultValue bool) bool {
	val := os.Getenv(key)
	if val == "" {
		return defaultValue
	}
	boolVal, err := strconv.ParseBool(val)
	if err != nil {
		return defaultValue
	}
	return boolVal
}

// GetDuration retrieves the value of the environment variable named by the key and converts it to a time.Duration.
// If the variable is empty, not present, or cannot be converted to a duration, it returns the specified default value.
func GetDuration(key string, defaultValue time.Duration) time.Duration {
	val := os.Getenv(key)
	if val == "" {
		return defaultValue
	}
	durationVal, err := time.ParseDuration(val)
	if err != nil {
		return defaultValue
	}
	return durationVal
}
