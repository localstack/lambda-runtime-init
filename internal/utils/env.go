package utils

import (
	"fmt"
	"os"
)

// GetEnvWithDefault returns the value of the environment variable key or the defaultValue if key is not set
func GetEnvWithDefault(key string, defaultValue string) string {
	envValue, ok := os.LookupEnv(key)
	if !ok {
		return defaultValue
	}

	return envValue
}

// GetEnv returns the value of the environment variable key or panics if the key is not set
func GetEnv(env string) (string, error) {
	result, found := os.LookupEnv(env)
	if !found {
		return "", fmt.Errorf("Could not find environment variable for: %s", env)
	}
	return result, nil
}

// MustGetEnv returns the value of the environment variable key or panics if the key is not set
func MustGetEnv(env string) string {
	result, found := os.LookupEnv(env)
	if !found {
		panic("Could not find environment variable for: " + env)
	}
	return result
}

// LookupEnvVars takes a slice of environment variable names and returns all existing and missing keys
// in two seperate slices.
func LookupEnvVars(envVars ...string) (existing []string, missing []string) {
	for _, envVar := range envVars {
		if value := os.Getenv(envVar); value == "" {
			missing = append(missing, envVar)
		} else {
			existing = append(existing, envVar)
		}
	}

	return existing, missing
}
