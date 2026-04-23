package homelab

import (
	"os"
	"strconv"
	"strings"
)

type ServerConfig struct {
	Protocol string
	Hostname string
	Port     int
}

func LoadServerConfig() ServerConfig {
	config := ServerConfig{
		Protocol: getEnvString("API_PROTOCOL", "http"),
		Hostname: getEnvString("API_HOSTNAME", "localhost"),
		Port:     getEnvInt("API_PORT", 3000),
	}

	return config, nil
}

func getEnvString(name, _default string) string {
	val := os.Getenv(name)
	if val == "" {
		return _default
	}

	val = strings.ReplaceAll(val, "\\n", "\n")

	return val
}

func getEnvInt(name string, _default int) int {
	val := os.Getenv(name)
	if val == "" {
		return _default
	}

	valInt, err := strconv.Atoi(val)
	if err != nil {
		return _default
	}

	return valInt
}

func getEnvBool(name string, _default bool) bool {
	val := os.Getenv(name)
	if val == "" {
		return _default
	}

	return strings.ToLower(val) == "true"
}
