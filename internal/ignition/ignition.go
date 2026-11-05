// SPDX-FileCopyrightText: SAP SE or an SAP affiliate company and IronCore contributors
// SPDX-License-Identifier: Apache-2.0

package ignition

import (
	"bytes"
	"context"
	_ "embed"
	"fmt"
	"text/template"
	"time"

	"github.com/ironcore-dev/metal-maintenance-operator/internal/token"
	metalv1alpha1 "github.com/ironcore-dev/metal-operator/api/v1alpha1"
)

var (
	//go:embed sanitization-ignition.yaml.gotmpl
	sanitizationIgnitionYAMLTemplateData string

	sanitizationIgnitionYAMLTemplate *template.Template
)

func init() {
	sanitizationIgnitionYAMLTemplate = template.Must(
		template.New("sanitization-ignition.yaml").Parse(sanitizationIgnitionYAMLTemplateData))
}

type SanitizationProvider struct {
	ReportBaseURL  string
	SigningKey      []byte
	TokenExpiry     time.Duration
}

func (p *SanitizationProvider) Ignition(
	_ context.Context,
	server *metalv1alpha1.Server,
	sanitizationUID string,
) ([]byte, error) {
	expiry := p.TokenExpiry
	if expiry <= 0 {
		expiry = time.Hour
	}

	tok, err := token.GenerateSignedDiscoveryToken(p.SigningKey, server.Spec.SystemUUID, expiry)
	if err != nil {
		return nil, fmt.Errorf("generate sanitization token: %w", err)
	}

	var buf bytes.Buffer
	if err := sanitizationIgnitionYAMLTemplate.Execute(&buf, struct {
		ReportURL string
		Token     string
	}{
		ReportURL: fmt.Sprintf("%s/%s", p.ReportBaseURL, sanitizationUID),
		Token:     tok,
	}); err != nil {
		return nil, fmt.Errorf("render sanitization ignition: %w", err)
	}
	return buf.Bytes(), nil
}
