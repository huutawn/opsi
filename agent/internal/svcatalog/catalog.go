package svcatalog

import (
	"fmt"
	"sort"
)

type Catalog struct {
	byType map[string]ServiceSchema
}

func BuiltInCatalog() Catalog {
	return NewCatalog(
		postgresSchema(),
		redisSchema(),
		kafkaSchema(),
		mysqlSchema(),
		mongoSchema(),
		rabbitMQSchema(),
	)
}

func NewCatalog(schemas ...ServiceSchema) Catalog {
	c := Catalog{byType: map[string]ServiceSchema{}}
	for _, schema := range schemas {
		c.byType[schema.Type] = schema
	}
	return c
}

func (c Catalog) Get(serviceType string) (ServiceSchema, bool) {
	schema, ok := c.byType[normalizeType(serviceType)]
	return schema, ok
}

func (c Catalog) Render(serviceType string, overrides map[string]string) (RenderedService, error) {
	schema, ok := c.Get(serviceType)
	if !ok {
		return RenderedService{}, fmt.Errorf("unknown service type %q", serviceType)
	}
	if overrides != nil && overrides["port"] != "" {
		if err := validatePort(overrides["port"]); err != nil {
			return RenderedService{}, err
		}
	}
	return schema.Render(overrides)
}

func (c Catalog) Types() []string {
	keys := make([]string, 0, len(c.byType))
	for key := range c.byType {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

func normalizeType(serviceType string) string {
	switch serviceType {
	case "postgres":
		return "postgresql"
	case "mongo":
		return "mongodb"
	case "rabbit":
		return "rabbitmq"
	default:
		return serviceType
	}
}

func postgresSchema() ServiceSchema {
	return ServiceSchema{
		Type:        "postgresql",
		DisplayName: "PostgreSQL",
		ConfigKeys: []ConfigKey{
			{Key: "version", Default: "16"},
			{Key: "database", Default: "app", Required: true, Pattern: `^[A-Za-z_][A-Za-z0-9_]*$`},
			{Key: "username", Default: "opsi", Required: true, Pattern: `^[A-Za-z_][A-Za-z0-9_]*$`},
			{Key: "storage_size", Default: "5Gi", Required: true},
			{Key: "port", Default: "5432", Required: true},
			{Key: "host"},
		},
		SecretKeys: []SecretKey{{Key: "password", AutoGenerate: true, Length: 32, Required: true}},
		EnvMapping: map[string]EnvTemplate{
			"DATABASE_URL":      "postgresql://{{.Username}}:{{.Password}}@{{.Host}}:{{.Port}}/{{.Database}}",
			"POSTGRES_HOST":     "{{.Host}}",
			"POSTGRES_PORT":     "{{.Port}}",
			"POSTGRES_DB":       "{{.Database}}",
			"POSTGRES_USER":     "{{.Username}}",
			"POSTGRES_PASSWORD": "{{.Password}}",
		},
	}
}

func redisSchema() ServiceSchema {
	return ServiceSchema{
		Type:        "redis",
		DisplayName: "Redis",
		ConfigKeys: []ConfigKey{
			{Key: "version", Default: "7-alpine"},
			{Key: "port", Default: "6379", Required: true},
			{Key: "max_memory", Default: "256mb"},
			{Key: "host"},
		},
		EnvMapping: map[string]EnvTemplate{
			"REDIS_URL":  "redis://{{.Host}}:{{.Port}}",
			"REDIS_HOST": "{{.Host}}",
			"REDIS_PORT": "{{.Port}}",
		},
	}
}

func kafkaSchema() ServiceSchema {
	return ServiceSchema{
		Type:        "kafka",
		DisplayName: "Kafka",
		ConfigKeys: []ConfigKey{
			{Key: "version", Default: "latest"},
			{Key: "port", Default: "9092", Required: true},
			{Key: "partitions", Default: "3"},
			{Key: "replication", Default: "1"},
			{Key: "host"},
		},
		EnvMapping: map[string]EnvTemplate{
			"KAFKA_BOOTSTRAP_SERVERS": "{{.Host}}:{{.Port}}",
			"KAFKA_BROKER":            "{{.Host}}:{{.Port}}",
		},
	}
}

func mysqlSchema() ServiceSchema {
	return ServiceSchema{
		Type:        "mysql",
		DisplayName: "MySQL",
		ConfigKeys: []ConfigKey{
			{Key: "version", Default: "8"},
			{Key: "database", Default: "app", Required: true, Pattern: `^[A-Za-z_][A-Za-z0-9_]*$`},
			{Key: "username", Default: "opsi", Required: true, Pattern: `^[A-Za-z_][A-Za-z0-9_]*$`},
			{Key: "storage_size", Default: "5Gi", Required: true},
			{Key: "port", Default: "3306", Required: true},
			{Key: "host"},
		},
		SecretKeys: []SecretKey{{Key: "password", AutoGenerate: true, Length: 32, Required: true}},
		EnvMapping: map[string]EnvTemplate{
			"DATABASE_URL":   "mysql://{{.Username}}:{{.Password}}@tcp({{.Host}}:{{.Port}})/{{.Database}}",
			"MYSQL_HOST":     "{{.Host}}",
			"MYSQL_PORT":     "{{.Port}}",
			"MYSQL_DATABASE": "{{.Database}}",
			"MYSQL_USER":     "{{.Username}}",
			"MYSQL_PASSWORD": "{{.Password}}",
		},
	}
}

func mongoSchema() ServiceSchema {
	return ServiceSchema{
		Type:        "mongodb",
		DisplayName: "MongoDB",
		ConfigKeys: []ConfigKey{
			{Key: "version", Default: "7"},
			{Key: "database", Default: "app", Required: true, Pattern: `^[A-Za-z_][A-Za-z0-9_]*$`},
			{Key: "username", Default: "opsi", Required: true, Pattern: `^[A-Za-z_][A-Za-z0-9_]*$`},
			{Key: "storage_size", Default: "5Gi", Required: true},
			{Key: "port", Default: "27017", Required: true},
			{Key: "host"},
		},
		SecretKeys: []SecretKey{{Key: "password", AutoGenerate: true, Length: 32, Required: true}},
		EnvMapping: map[string]EnvTemplate{
			"MONGO_URI":      "mongodb://{{.Username}}:{{.Password}}@{{.Host}}:{{.Port}}/{{.Database}}",
			"MONGO_HOST":     "{{.Host}}",
			"MONGO_PORT":     "{{.Port}}",
			"MONGO_DATABASE": "{{.Database}}",
			"MONGO_USER":     "{{.Username}}",
			"MONGO_PASSWORD": "{{.Password}}",
		},
	}
}

func rabbitMQSchema() ServiceSchema {
	return ServiceSchema{
		Type:        "rabbitmq",
		DisplayName: "RabbitMQ",
		ConfigKeys: []ConfigKey{
			{Key: "version", Default: "3-management"},
			{Key: "username", Default: "opsi", Required: true, Pattern: `^[A-Za-z_][A-Za-z0-9_]*$`},
			{Key: "vhost", Default: "app", Required: true},
			{Key: "port", Default: "5672", Required: true},
			{Key: "host"},
		},
		SecretKeys: []SecretKey{{Key: "password", AutoGenerate: true, Length: 32, Required: true}},
		EnvMapping: map[string]EnvTemplate{
			"AMQP_URL":              "amqp://{{.Username}}:{{.Password}}@{{.Host}}:{{.Port}}/{{.Vhost}}",
			"RABBITMQ_HOST":         "{{.Host}}",
			"RABBITMQ_PORT":         "{{.Port}}",
			"RABBITMQ_DEFAULT_USER": "{{.Username}}",
			"RABBITMQ_DEFAULT_PASS": "{{.Password}}",
		},
	}
}
