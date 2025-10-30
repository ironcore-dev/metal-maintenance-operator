// SPDX-FileCopyrightText: 2025 SAP SE or an SAP affiliate company and IronCore contributors
//
// SPDX-License-Identifier: Apache-2.0

package client

type AuthMethod string

const (
	HPEToken  AuthMethod = "auth"
	BasicAuth AuthMethod = "Authorization"
	DellToken AuthMethod = "X-Auth-Token"
	None      AuthMethod = ""
)
