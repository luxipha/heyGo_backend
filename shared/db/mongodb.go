package db

import (
	"context"
	"time"

	"go.mongodb.org/mongo-driver/v2/mongo"
	"go.mongodb.org/mongo-driver/v2/mongo/options"
)

type DBConfig struct {
	URI             string
	Database        string
	MaxConnIdleTime time.Duration
	MaxPoolSize     uint64
	MinPoolSize     uint64
}

func NewMongoDBClient(cfg DBConfig) (*mongo.Client, error) {
	opts := &options.ClientOptions{
		MaxConnIdleTime: &cfg.MaxConnIdleTime,
		MaxPoolSize:     &cfg.MaxPoolSize,
		MinPoolSize:     &cfg.MinPoolSize,
	}

	opts = opts.ApplyURI(cfg.URI)

	client, err := mongo.Connect(opts)
	if err != nil {
		return nil, err
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	if err := client.Ping(ctx, nil); err != nil {
		client.Disconnect(ctx)
		return nil, err
	}

	return client, nil
}
