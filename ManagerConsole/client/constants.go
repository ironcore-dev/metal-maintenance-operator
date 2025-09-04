package client

type AuthMethod string

const (
	HPEToken  AuthMethod = "auth"
	BasicAuth AuthMethod = "Authorization"
	DellToken AuthMethod = "X-Auth-Token"
	None      AuthMethod = ""
)
