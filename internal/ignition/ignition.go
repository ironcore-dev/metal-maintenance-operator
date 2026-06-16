// SPDX-FileCopyrightText: 2025 SAP SE or an SAP affiliate company and IronCore contributors
// SPDX-License-Identifier: Apache-2.0

package ignition

import (
	"bytes"
	"context"
	_ "embed"
	"fmt"
	"text/template"

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
	ReportBaseURL string
}

func (p *SanitizationProvider) Ignition(
	_ context.Context,
	_ *metalv1alpha1.Server,
	sanitizationUID string,
) ([]byte, error) {
	var buf bytes.Buffer
	if err := sanitizationIgnitionYAMLTemplate.Execute(&buf, struct {
		ReportURL string
	}{ReportURL: fmt.Sprintf("%s/%s", p.ReportBaseURL, sanitizationUID)}); err != nil {
		return nil, fmt.Errorf("render sanitization ignition: %w", err)
	}
	return buf.Bytes(), nil
}
