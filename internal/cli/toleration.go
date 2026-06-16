// SPDX-FileCopyrightText: 2025 SAP SE or an SAP affiliate company and IronCore contributors
// SPDX-License-Identifier: Apache-2.0

package cli

import (
	"bytes"
	"encoding/csv"
	"fmt"
	"strings"

	metalv1alpha1 "github.com/ironcore-dev/metal-operator/api/v1alpha1"
	"github.com/spf13/pflag"
)

func readAsCSV(val string) ([]string, error) {
	if val == "" {
		return []string{}, nil
	}
	stringReader := strings.NewReader(val)
	csvReader := csv.NewReader(stringReader)
	return csvReader.Read()
}

func writeAsCSV(vals []string) (string, error) {
	b := &bytes.Buffer{}
	w := csv.NewWriter(b)
	err := w.Write(vals)
	if err != nil {
		return "", err
	}
	w.Flush()
	return strings.TrimSuffix(b.String(), "\n"), nil
}

type tolerationsVar struct {
	changed bool
	value   *[]metalv1alpha1.Toleration
}

func newTolerationsVar(val []metalv1alpha1.Toleration, p *[]metalv1alpha1.Toleration) *tolerationsVar {
	ssv := new(tolerationsVar)
	ssv.value = p
	*ssv.value = val
	return ssv
}

func TolerationsVar(p *[]metalv1alpha1.Toleration, name string, value []metalv1alpha1.Toleration, usage string) {
	pflag.Var(newTolerationsVar(value, p), name, usage)
}

func readTolerationsAsCSV(val string) ([]metalv1alpha1.Toleration, error) {
	v, err := readAsCSV(val)
	if err != nil {
		return nil, err
	}

	res := make([]metalv1alpha1.Toleration, 0, len(v))
	for _, v := range v {
		kv := strings.SplitN(v, "=", 2)
		if len(kv) != 2 {
			return nil, fmt.Errorf("%s must be formatted as key=value", v)
		}

		key := kv[0]
		ve := strings.SplitN(kv[1], ":", 2)
		if len(ve) != 2 {
			return nil, fmt.Errorf("%s must be formatted as value:effect", ve)
		}

		value := ve[0]
		effect := ve[1]
		operator := metalv1alpha1.TolerationOperatorEqual
		if value == "" {
			operator = metalv1alpha1.TolerationOperatorExists
		}

		res = append(res, metalv1alpha1.Toleration{
			Key:      key,
			Operator: operator,
			Value:    value,
			Effect:   metalv1alpha1.TaintEffect(effect),
		})
	}
	return res, nil
}

func writeTolerationsAsCSV(tolerations []metalv1alpha1.Toleration) (string, error) {
	s := make([]string, 0, len(tolerations))
	for _, v := range tolerations {
		s = append(s, fmt.Sprintf("%s=%s:%s", v.Key, v.Value, v.Effect))
	}
	return writeAsCSV(s)
}

func (t *tolerationsVar) Type() string {
	return "tolerations"
}

func (t *tolerationsVar) String() string {
	str, _ := writeTolerationsAsCSV(*t.value)
	return fmt.Sprintf("[%s]", str)
}

func (t *tolerationsVar) Set(val string) error {
	v, err := readTolerationsAsCSV(val)
	if err != nil {
		return err
	}

	if !t.changed {
		*t.value = v
	} else {
		*t.value = append(*t.value, v...)
	}
	t.changed = true
	return nil
}
