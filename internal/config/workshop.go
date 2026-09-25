package config

import (
	"errors"
	"fmt"
	"net/url"
	"strings"
	"time"
)

type WorkshopConfig struct {
	Enabled        bool
	BaseURL        string
	AuthToken      string
	RequestTimeout time.Duration
}

type rawWorkshopConfig struct {
	Enabled        bool   `yaml:"enabled"`
	BaseURL        string `yaml:"base_url"`
	AuthToken      string `yaml:"auth_token"`
	RequestTimeout string `yaml:"request_timeout"`
}

func loadWorkshop(raw rawWorkshopConfig, lookup func(string) (string, bool)) (WorkshopConfig, error) {
	if !raw.Enabled {
		return WorkshopConfig{}, nil
	}
	base := strings.TrimRight(strings.TrimSpace(raw.BaseURL), "/")
	u, err := url.Parse(base)
	if err != nil || u.Host == "" || (u.Scheme != "http" && u.Scheme != "https") || u.User != nil || u.RawQuery != "" || u.Fragment != "" {
		return WorkshopConfig{}, errors.New("workshop.base_url must be an HTTP URL without credentials, query or fragment")
	}
	// An agent connection always authenticates, even when both processes are local.
	token, err := resolveEnvironmentReference(raw.AuthToken, "workshop.auth_token", lookup)
	if err != nil {
		return WorkshopConfig{}, err
	}
	if raw.RequestTimeout == "" {
		raw.RequestTimeout = "15s"
	}
	timeout, err := parseDuration(raw.RequestTimeout)
	if err != nil {
		return WorkshopConfig{}, fmt.Errorf("workshop.request_timeout: %w", err)
	}
	return WorkshopConfig{Enabled: true, BaseURL: base, AuthToken: token, RequestTimeout: timeout}, nil
}
