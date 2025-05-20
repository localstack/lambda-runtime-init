package utils

import "os"

// GetEnvWithDefault returns the value of the environment variable key or the defaultValue if key is not set
func GetEnvWithDefault(key string, defaultValue string) string {
	envValue, ok := os.LookupEnv(key)
	if !ok {
		return defaultValue
	}

	return envValue
}

// GetEnvOrDie returns the value of the environment variable key or panics if the key is not set
func GetEnvOrDie(env string) string {
	result, found := os.LookupEnv(env)
	if !found {
		panic("Could not find environment variable for: " + env)
	}
	return result
}
