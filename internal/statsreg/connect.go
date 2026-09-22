package statsreg

import (
	"context"
	"crypto/tls"
	"database/sql"
	"fmt"
	"net"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/go-sql-driver/mysql"
	"github.com/google/uuid"
	"github.com/redis/go-redis/v9"
	"github.com/segmentio/kafka-go"
	"github.com/segmentio/kafka-go/sasl/plain"
)

type catalog struct {
	TypeID, InterfaceID, DriverID int64
	Templates                     []template
}
type session struct {
	Config  Config
	Plan    Plan
	Catalog catalog
	Expires time.Time
}
type Service struct {
	mu        sync.Mutex
	sessions  map[string]session
	Directory string
	// Progress receives one-line status updates during a registration.
	Progress func(string)
	// Zero means the defaults (defaultSettle / verifyWindow); tests shorten them.
	SettleWait   time.Duration
	VerifyWindow time.Duration
	// Registration patterns file; empty means ~/.rtaskmgr/stats-patterns.json.
	PatternFile string
	// Saved connections file; empty means ~/.rtaskmgr/stats-connections.json.
	ConnectionFile string
}

func openDB(ctx context.Context, c Config) (*sql.DB, error) {
	if strings.TrimSpace(c.DBHost) == "" || c.DBPort < 1 || c.DBPort > 65535 {
		return nil, fmt.Errorf("MariaDB 주소와 포트를 확인하세요")
	}
	dsn := mysql.NewConfig()
	dsn.User = c.DBUser
	dsn.Passwd = c.DBPassword
	dsn.Net = "tcp"
	dsn.Addr = net.JoinHostPort(c.DBHost, strconv.Itoa(c.DBPort))
	dsn.DBName = "liz"
	dsn.Timeout = 8 * time.Second
	dsn.ReadTimeout = 30 * time.Second
	dsn.WriteTimeout = 30 * time.Second
	dsn.ParseTime = true
	dsn.Loc = time.Local
	db, err := sql.Open("mysql", dsn.FormatDSN())
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(3)
	db.SetConnMaxLifetime(5 * time.Minute)
	if err = db.PingContext(ctx); err != nil {
		db.Close()
		return nil, fmt.Errorf("MariaDB 연결 실패: %w", err)
	}
	return db, nil
}
func brokers(c Config) []string {
	var out []string
	for _, v := range strings.FieldsFunc(c.Brokers, func(r rune) bool { return r == ',' || r == '\n' || r == ' ' }) {
		if v != "" {
			out = append(out, v)
		}
	}
	return out
}
func kafkaDialer(c Config) (*kafka.Dialer, error) {
	tcpDial, err := kafkaTCPDial(c)
	if err != nil {
		return nil, err
	}
	d := &kafka.Dialer{Timeout: 8 * time.Second, DialFunc: tcpDial}
	switch c.KafkaSecurity {
	case "", "PLAINTEXT":
	case "SSL":
		d.TLS = &tls.Config{MinVersion: tls.VersionTLS12}
	case "SASL_PLAINTEXT":
		d.SASLMechanism = plain.Mechanism{Username: c.KafkaUser, Password: c.KafkaPassword}
	case "SASL_SSL":
		d.TLS = &tls.Config{MinVersion: tls.VersionTLS12}
		d.SASLMechanism = plain.Mechanism{Username: c.KafkaUser, Password: c.KafkaPassword}
	default:
		return nil, fmt.Errorf("지원하지 않는 Kafka 보안 방식")
	}
	return d, nil
}
func checkKafka(ctx context.Context, c Config) error {
	d, err := kafkaDialer(c)
	if err != nil {
		return err
	}
	var last error
	for _, addr := range brokers(c) {
		conn, e := d.DialContext(ctx, "tcp", addr)
		if e != nil {
			last = e
			continue
		}
		conn.SetDeadline(time.Now().Add(10 * time.Second))
		parts, e := conn.ReadPartitions("liz.message.pipeline")
		conn.Close()
		if e != nil {
			last = e
			continue
		}
		if len(parts) == 0 {
			last = fmt.Errorf("liz.message.pipeline 토픽이 없습니다")
			continue
		}
		leader, e := d.DialLeader(ctx, "tcp", addr, "liz.message.pipeline", parts[0].ID)
		if e != nil {
			last = e
			continue
		}
		leader.Close()
		return nil
	}
	if last == nil {
		last = fmt.Errorf("브로커 주소를 입력하세요")
	}
	return fmt.Errorf("Kafka 연결 실패: %w", last)
}

type queryer interface {
	QueryContext(context.Context, string, ...any) (*sql.Rows, error)
	QueryRowContext(context.Context, string, ...any) *sql.Row
}

func options(ctx context.Context, q queryer, query string) ([]Option, error) {
	rows, e := q.QueryContext(ctx, query)
	if e != nil {
		return nil, e
	}
	defer rows.Close()
	out := []Option{}
	for rows.Next() {
		var v Option
		if e = rows.Scan(&v.ID, &v.Name); e != nil {
			return nil, e
		}
		out = append(out, v)
	}
	return out, rows.Err()
}
func uniqueID(ctx context.Context, q queryer, query string) (int64, error) {
	opts, e := options(ctx, q, query)
	if e != nil {
		return 0, e
	}
	if len(opts) != 1 {
		return 0, fmt.Errorf("필수 카탈로그가 없거나 중복입니다: %s", query)
	}
	return opts[0].ID, nil
}
func loadCatalog(ctx context.Context, db queryer) (catalog, error) {
	var c catalog
	var e error
	c.TypeID, e = uniqueID(ctx, db, "SELECT t.id,t.name FROM device_type t JOIN device_category c ON c.id=t.category_id WHERE t.code='MK119_SYSTEM' AND c.code='ETC' AND t.flag=1")
	if e != nil {
		return c, e
	}
	c.InterfaceID, e = uniqueID(ctx, db, "SELECT id,name FROM interface_type WHERE code='MK119_CACHE'")
	if e != nil {
		return c, e
	}
	c.DriverID, e = uniqueID(ctx, db, "SELECT id,name FROM support_device_driver WHERE name='MK119 v10 System Stats' AND flag=1")
	if e != nil {
		return c, e
	}
	rows, e := db.QueryContext(ctx, "SELECT id,name,COALESCE(request_command,driver_code,''),COALESCE(measure,''),COALESCE(data_format,4),COALESCE(default_interval,10000) FROM support_checkpoint WHERE support_device_driver_id=? AND enable_to_use=1", c.DriverID)
	if e != nil {
		return c, e
	}
	defer rows.Close()
	for rows.Next() {
		var t template
		if e = rows.Scan(&t.ID, &t.Name, &t.Pattern, &t.Measure, &t.Format, &t.Interval); e != nil {
			return c, e
		}
		c.Templates = append(c.Templates, t)
	}
	return c, rows.Err()
}

func (s *Service) Inspect(ctx context.Context, c Config) (Plan, error) {
	if c.RedisHost == "" || c.RedisPort < 1 || c.RedisPort > 65535 || c.RedisDB < 0 {
		return Plan{}, fmt.Errorf("Redis 주소/포트/DB를 확인하세요")
	}
	db, e := openDB(ctx, c)
	if e != nil {
		return Plan{}, e
	}
	defer db.Close()
	r, e := openRedis(ctx, c)
	if e != nil {
		return Plan{}, e
	}
	defer r.Close()
	if e = checkKafka(ctx, c); e != nil {
		return Plan{}, e
	}
	cat, e := loadCatalog(ctx, db)
	if e != nil {
		return Plan{}, e
	}
	p := Plan{ID: uuid.NewString(), GroupName: "System", Servers: []Server{}, Warnings: []string{}, RedisInfo: r.Info}
	p.Explorers, e = options(ctx, db, "SELECT id,CONCAT(name,' (DC ',data_center_id,')') FROM device_explorer WHERE data_center_id IS NOT NULL ORDER BY id")
	if e != nil {
		return p, e
	}
	p.Clusters, e = options(ctx, db, "SELECT id,name FROM liz_cluster WHERE cluster_type=3 ORDER BY id")
	if e != nil {
		return p, e
	}
	if len(p.Explorers) == 0 || len(p.Clusters) == 0 {
		return p, fmt.Errorf("장비 탐색기 또는 Collector 클러스터가 없습니다")
	}
	rows, e := db.QueryContext(ctx, "SELECT hostname,COALESCE(ip_address,'') FROM liz_server ORDER BY id")
	if e != nil {
		return p, e
	}
	listed := map[string]bool{}
	for rows.Next() {
		var v Server
		if e = rows.Scan(&v.Hostname, &v.IP); e != nil {
			rows.Close()
			return p, e
		}
		// liz_server can list one hostname several times (seen on AWS: 4 rows).
		if v.Hostname != "" && !listed[v.Hostname] {
			listed[v.Hostname] = true
			p.Servers = append(p.Servers, v)
		}
	}
	e = rows.Err()
	rows.Close()
	if e != nil {
		return p, e
	}
	mods := []module{}
	rows, e = db.QueryContext(ctx, "SELECT id,COALESCE(hostname,'') FROM liz_module")
	if e != nil {
		return p, e
	}
	for rows.Next() {
		var m module
		if e = rows.Scan(&m.ID, &m.Host); e != nil {
			rows.Close()
			return p, e
		}
		mods = append(mods, m)
	}
	e = rows.Err()
	rows.Close()
	if e != nil {
		return p, e
	}
	known := map[string]bool{}
	for _, v := range p.Servers {
		known[v.Hostname] = true
	}
	for _, m := range mods {
		if m.Host != "" && !known[m.Host] {
			p.Servers = append(p.Servers, Server{Hostname: m.Host})
			known[m.Host] = true
		}
	}
	sort.SliceStable(p.Servers, func(i, j int) bool { return p.Servers[i].Hostname < p.Servers[j].Hostname })
	for i := range p.Servers {
		p.Servers[i].Name = p.Servers[i].Hostname
	}
	values := map[string]string{}
	keys, e := r.scanKeys(ctx, "liz.stats.*", 10000)
	if e != nil {
		return p, e
	}
	for _, key := range keys {
		typ, err := r.Type(ctx, key).Result()
		if err != nil {
			return p, err
		}
		if typ == "none" {
			continue
		}
		if typ != "string" {
			p.Warnings = append(p.Warnings, key+": 문자열 타입이 아니어서 제외")
			continue
		}
		size, err := r.StrLen(ctx, key).Result()
		if err != nil {
			return p, err
		}
		if size > 65536 {
			p.Warnings = append(p.Warnings, key+": 값이 64KB를 넘어 제외")
			continue
		}
		v, err := r.Get(ctx, key).Result()
		if err == redis.Nil {
			continue
		}
		if err != nil {
			return p, err
		}
		values[key] = v
	}
	p.Points = makePoints(values, p.Servers, mods, cat.Templates)
	p.Servers = append(p.Servers, Server{Hostname: "common", Name: "common"})
	s.applyPatterns(&p)
	regs, e := loadExisting(ctx, db, cat.DriverID)
	if e != nil {
		return p, fmt.Errorf("기존 등록 조회 실패: %w", e)
	}
	if len(regs) > 0 {
		regValues := map[string]string{}
		for _, r := range regs {
			regValues[r.key] = ""
		}
		placeOf := map[string]string{}
		for _, pt := range makePoints(regValues, p.Servers[:len(p.Servers)-1], mods, cat.Templates) {
			placeOf[pt.Key] = pt.Host
		}
		markExisting(&p, regs, placeOf, hostAnchors(regValues, p.Servers))
		for i := range p.Existing {
			if p.Existing[i].ClusterID, e = groupCluster(ctx, db, p.Existing[i].GroupID); e != nil {
				return p, fmt.Errorf("기존 그룹의 Collector 클러스터 조회 실패: %w", e)
			}
		}
	}
	deleted, e := deletedKeys(ctx, db, cat.DriverID)
	if e != nil {
		return p, fmt.Errorf("삭제된 등록 조회 실패: %w", e)
	}
	markDeleted(&p, deleted)
	if len(p.Points) == 0 {
		return p, fmt.Errorf("Redis에 liz.stats.* 키가 없습니다")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.sessions == nil {
		s.sessions = map[string]session{}
	}
	for id, v := range s.sessions {
		if time.Now().After(v.Expires) {
			delete(s.sessions, id)
		}
	}
	s.sessions[p.ID] = session{Config: c, Plan: p, Catalog: cat, Expires: time.Now().Add(30 * time.Minute)}
	return p, nil
}
