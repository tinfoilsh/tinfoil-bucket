package main

import "os"

type Config struct {
	ListenAddr          string
	ControlPlaneURL     string
	UsageReporterID     string
	UsageReporterSecret string
}

func Load() *Config {
	return &Config{
		ListenAddr:          getEnv("LISTEN_ADDR", ":8080"),
		ControlPlaneURL:     getEnv("CONTROL_PLANE_URL", "https://api.tinfoil.sh"),
		UsageReporterID:     getEnv("USAGE_REPORTER_ID", "tinfoil-bucket"),
		UsageReporterSecret: os.Getenv("USAGE_REPORTER_SECRET"),
	}
}

func getEnv(key, fallback string) string {
	if val := os.Getenv(key); val != "" {
		return val
	}
	return fallback
}
