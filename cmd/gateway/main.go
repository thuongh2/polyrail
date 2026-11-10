package main

import (
	_ "github.com/cockroachdb/pebble/v2"
	_ "github.com/coreos/go-oidc/v3/oidc"
	_ "github.com/jackc/pgx/v5/stdlib"
	_ "github.com/maximhq/bifrost/core"
	_ "github.com/redis/go-redis/v9"
	_ "golang.org/x/crypto/bcrypt"
)

func main() {}
