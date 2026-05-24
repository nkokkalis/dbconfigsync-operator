package dbclient

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	_ "github.com/go-sql-driver/mysql"
	_ "github.com/lib/pq"
	"github.com/redis/go-redis/v9"
	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/mongo"
	"go.mongodb.org/mongo-driver/mongo/options"
)

// FetchConfig queries the specified database and returns key-value configuration pairs.
func FetchConfig(ctx context.Context, dbType, connectionUri, query string) (map[string]string, error) {
	switch strings.ToLower(dbType) {
	case "postgresql", "postgres":
		return fetchSQL(ctx, "postgres", connectionUri, query)
	case "mysql":
		return fetchSQL(ctx, "mysql", connectionUri, query)
	case "redis":
		return fetchRedis(ctx, connectionUri, query)
	case "mongodb", "mongo":
		return fetchMongo(ctx, connectionUri, query)
	default:
		return nil, fmt.Errorf("unsupported database type: %s", dbType)
	}
}

// fetchSQL connects to a relational database (Postgres or MySQL) and executes a query.
// It detects returned columns dynamically and maps them either as:
// - Rows of Key/Value if columns are named key/value (allows multiple configurations).
// - Or mapping Column Names directly to values (if arbitrary table query).
func fetchSQL(ctx context.Context, driverName, connectionUri, query string) (map[string]string, error) {
	db, err := sql.Open(driverName, connectionUri)
	if err != nil {
		return nil, fmt.Errorf("failed to open database: %w", err)
	}
	defer func() { _ = db.Close() }()

	db.SetConnMaxLifetime(10 * time.Second)
	
	// Check connection
	if err := db.PingContext(ctx); err != nil {
		return nil, fmt.Errorf("failed to ping database: %w", err)
	}

	rows, err := db.QueryContext(ctx, query)
	if err != nil {
		return nil, fmt.Errorf("failed to execute query: %w", err)
	}
	defer func() { _ = rows.Close() }()

	cols, err := rows.Columns()
	if err != nil {
		return nil, fmt.Errorf("failed to get columns: %w", err)
	}

	configs := make(map[string]string)
	
	// Normalize column names to check key-value pair format
	isKeyValueFormat := false
	if len(cols) == 2 {
		col0 := strings.ToLower(cols[0])
		col1 := strings.ToLower(cols[1])
		if (col0 == "key" || col0 == "config_key" || col0 == "key_name" || col0 == "name") &&
			(col1 == "value" || col1 == "config_value" || col1 == "val" || col1 == "val_value") {
			isKeyValueFormat = true
		}
	}

	// Prepare dynamic scan target interfaces
	scanArgs := make([]interface{}, len(cols))
	values := make([]sql.RawBytes, len(cols))
	for i := range values {
		scanArgs[i] = &values[i]
	}

	rowIdx := 0
	for rows.Next() {
		if err := rows.Scan(scanArgs...); err != nil {
			return nil, fmt.Errorf("failed to scan row: %w", err)
		}

		if isKeyValueFormat {
			// Key-Value row format
			key := ""
			val := ""
			if values[0] != nil {
				key = string(values[0])
			}
			if values[1] != nil {
				val = string(values[1])
			}
			if key != "" {
				configs[key] = val
			}
		} else {
			// Column-to-Key mapping format
			for i, colName := range cols {
				val := ""
				if values[i] != nil {
					val = string(values[i])
				}
				// Default key name is uppercase column name
				keyName := strings.ToUpper(colName)
				if rowIdx == 0 {
					configs[keyName] = val
				} else {
					// Append row index suffix for multi-row queries to avoid overwriting
					configs[fmt.Sprintf("%s_%d", keyName, rowIdx)] = val
				}
			}
		}
		rowIdx++
	}

	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("error during row iteration: %w", err)
	}

	return configs, nil
}

// fetchRedis parses the query and connects to Redis.
// Supported queries:
// - "HGETALL <hash_key>": Fetches all fields in a hash.
// - "GET <key>": Fetches a single key's value (parsed as JSON KV pair, or stored as key=value).
func fetchRedis(ctx context.Context, connectionUri, query string) (map[string]string, error) {
	opt, err := redis.ParseURL(connectionUri)
	if err != nil {
		return nil, fmt.Errorf("failed to parse redis connection URI: %w", err)
	}

	rdb := redis.NewClient(opt)
	defer func() { _ = rdb.Close() }()

	// Ping check
	if err := rdb.Ping(ctx).Err(); err != nil {
		return nil, fmt.Errorf("failed to ping redis: %w", err)
	}

	parts := strings.Fields(query)
	if len(parts) < 2 {
		return nil, fmt.Errorf("invalid redis query format, expected 'HGETALL key' or 'GET key'")
	}

	cmd := strings.ToUpper(parts[0])
	key := parts[1]
	configs := make(map[string]string)

	switch cmd {
	case "HGETALL":
		res, err := rdb.HGetAll(ctx, key).Result()
		if err != nil {
			return nil, fmt.Errorf("redis HGETALL failed for key %s: %w", key, err)
		}
		return res, nil
	case "GET":
		res, err := rdb.Get(ctx, key).Result()
		if err != nil {
			return nil, fmt.Errorf("redis GET failed for key %s: %w", key, err)
		}
		// Try parsing as JSON first
		var jsonMap map[string]interface{}
		if err := json.Unmarshal([]byte(res), &jsonMap); err == nil {
			for k, v := range jsonMap {
				configs[k] = fmt.Sprintf("%v", v)
			}
		} else {
			// If not JSON, save key as query key, and value as string
			configs[key] = res
		}
		return configs, nil
	default:
		return nil, fmt.Errorf("unsupported redis command: %s (supported: HGETALL, GET)", cmd)
	}
}

type MongoQuery struct {
	DB         string                 `json:"db"`
	Collection string                 `json:"collection"`
	Filter     map[string]interface{} `json:"filter"`
}

// fetchMongo connects to MongoDB and runs a find query.
// It parses the JSON-formatted query parameter:
// {"db": "database", "collection": "collection_name", "filter": {"key": "val"}}
func fetchMongo(ctx context.Context, connectionUri, query string) (map[string]string, error) {
	var mq MongoQuery
	if err := json.Unmarshal([]byte(query), &mq); err != nil {
		return nil, fmt.Errorf("failed to parse MongoDB query JSON: %w (format: {\"db\": \"...\", \"collection\": \"...\", \"filter\": {...}})", err)
	}

	if mq.DB == "" || mq.Collection == "" {
		return nil, fmt.Errorf("mongodb query must specify 'db' and 'collection'")
	}

	client, err := mongo.Connect(ctx, options.Client().ApplyURI(connectionUri))
	if err != nil {
		return nil, fmt.Errorf("failed to connect to mongodb: %w", err)
	}
	defer func() {
		_ = client.Disconnect(ctx)
	}()

	// Ping database
	if err := client.Ping(ctx, nil); err != nil {
		return nil, fmt.Errorf("failed to ping mongodb: %w", err)
	}

	coll := client.Database(mq.DB).Collection(mq.Collection)
	
	// Convert filter map to bson.M
	filterBson := bson.M{}
	for k, v := range mq.Filter {
		filterBson[k] = v
	}

	cursor, err := coll.Find(ctx, filterBson)
	if err != nil {
		return nil, fmt.Errorf("mongodb find query failed: %w", err)
	}
	defer func() { _ = cursor.Close(ctx) }()

	configs := make(map[string]string)
	for cursor.Next(ctx) {
		var doc bson.M
		if err := cursor.Decode(&doc); err != nil {
			return nil, fmt.Errorf("failed to decode mongo document: %w", err)
		}

		// Document parsing strategy:
		// 1. If document has explicit "key" and "value" fields, extract them.
		// 2. Otherwise, extract all top-level primitive fields as configuration keys.
		kVal, okKey := doc["key"].(string)
		vVal, okVal := doc["value"]
		if okKey && okVal {
			configs[kVal] = fmt.Sprintf("%v", vVal)
		} else {
			for k, v := range doc {
				// Skip _id and non-primitive fields to keep configuration simple
				if k == "_id" {
					continue
				}
				switch valType := v.(type) {
				case string:
					configs[k] = valType
				case int32, int64, float64, bool:
					configs[k] = fmt.Sprintf("%v", v)
				}
			}
		}
	}

	if err := cursor.Err(); err != nil {
		return nil, fmt.Errorf("error during mongodb cursor iteration: %w", err)
	}

	return configs, nil
}
