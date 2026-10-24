package doctor

import (
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net/url"
	"os"
	"path/filepath"
	"strings"

	_ "modernc.org/sqlite"

	"github.com/railse/railse-opc/internal/agent"
	"github.com/railse/railse-opc/internal/hub/push"
	"github.com/railse/railse-opc/internal/hub/store"
	"github.com/railse/railse-opc/internal/hub/wire"
)

// hubClientChecks 在机器侧看 hub 是否可达、协议是否一致：GET machine.json 里 hub_url 的 /api/version，不带凭据。
func hubClientChecks(o Options) []Check {
	const id, name = "hub.reachable", "hub 可达"
	// 与守护进程用同一个解析与校验；它的错误信息不带文件内容（里面有本机密钥）
	m, err := agent.LoadMachine(filepath.Join(o.AgentDir, "machine.json"))
	if errors.Is(err, fs.ErrNotExist) {
		return []Check{{ID: id, Name: name, Status: Skip, Detail: "没有 machine.json（未接入 hub）"}}
	}
	if err != nil {
		return []Check{{ID: id, Name: name, Status: Fail, Detail: err.Error(), Hint: "railse agent enroll --hub … --token … --force 重新接入"}}
	}
	endpoint := strings.TrimRight(m.HubURL, "/") + "/api/version"
	resp, err := o.HTTP.Get(endpoint)
	if err != nil {
		return []Check{{ID: id, Name: name, Status: Fail, Detail: err.Error(), Hint: "确认 hub 在跑、地址与端口对、这台机器访问得到它（证书错误时看 hub 的 TLS 配置）"}}
	}
	defer resp.Body.Close()
	var v struct {
		Version    string `json:"version"`
		AgentProto int    `json:"agent_proto"`
	}
	if resp.StatusCode != 200 {
		return []Check{{ID: id, Name: name, Status: Fail, Detail: fmt.Sprintf("%s 回 %d", endpoint, resp.StatusCode), Hint: "hub 太旧（没有 /api/version）、地址指向了别的服务，或被重定向（hub_url 应写最终地址）；升级 hub 或 railse agent enroll --force 改地址"}}
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 64<<10)).Decode(&v); err != nil {
		return []Check{{ID: id, Name: name, Status: Fail, Detail: endpoint + " 回的不是 hub 的版本信息", Hint: "确认 hub_url 指向 railse hub，而不是别的服务"}}
	}
	if v.AgentProto != wire.Proto {
		return []Check{{ID: id, Name: name, Status: Fail, Detail: fmt.Sprintf("hub 协议 %d，本机 %d（hub 版本 %s，本机 %s）", v.AgentProto, wire.Proto, v.Version, o.Version), Hint: "请升级较旧的一方，让 hub 与本机是同一版本"}}
	}
	return []Check{{ID: id, Name: name, Status: OK, Detail: fmt.Sprintf("%s，hub 版本 %s", m.HubURL, v.Version)}}
}

// hubServerChecks 在 hub 机器上看数据目录、数据库、密码、推送密钥、ACME 目录与开机自启。只读打开数据库，不跑迁移。
func hubServerChecks(o Options) []Check {
	items := []struct{ id, name string }{
		{"hubsrv.dir", "hub 数据目录"}, {"hubsrv.db", "hub 数据库"}, {"hubsrv.password", "网页密码"},
		{"hubsrv.vapid", "推送密钥"}, {"hubsrv.acme", "ACME 证书目录"}, {"hubsrv.autostart", "hub 开机自启"},
	}
	skipAll := func(detail string) []Check {
		var out []Check
		for _, it := range items {
			out = append(out, Check{ID: it.id, Name: it.name, Status: Skip, Detail: detail})
		}
		return out
	}
	if o.HubDir == "" {
		return skipAll("这台机器没有 hub 数据目录")
	}
	if _, err := os.Stat(o.HubDir); err != nil {
		return skipAll("没有 hub 数据目录 " + o.HubDir)
	}
	out := []Check{permCheck("hubsrv.dir", "hub 数据目录", o.HubDir, 0o700, true, "railse hub run 会建好")}
	out = append(out, hubDBChecks(o)...)
	out = append(out, acmeCheck(o))
	out = append(out, autostartCheck("hubsrv.autostart", "hub 开机自启", o, "hub"))
	return out
}

// openReadOnly 只读打开 hub.db，不建任何文件。hub 没在跑（没有 -wal）时加 immutable=1：SQLite 不加锁、不建 -wal 与 -shm
// （单用 mode=ro 打开 WAL 库也会建出 -wal、-shm 并留下，sudo 跑 doctor 时属主还是 root，hub 之后就写不了库）。
// hub 在跑时 -wal 与 -shm 本来就在，用 mode=ro 才读得到还在 WAL 里、没检查点的数据。
// 路径按 URL 编码（同 store.Open），路径里的 %、#、? 不会被当成转义、片段或参数。
func openReadOnly(path string) (*sql.DB, error) {
	abs, err := filepath.Abs(path)
	if err != nil {
		return nil, err
	}
	query := "mode=ro&immutable=1"
	if _, err := os.Lstat(abs + "-wal"); err == nil {
		query = "mode=ro"
	}
	db, err := sql.Open("sqlite", (&url.URL{Scheme: "file", Path: abs, RawQuery: query}).String())
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	return db, nil
}

// hubDBChecks 给出 hubsrv.db、hubsrv.password、hubsrv.vapid 三项。
func hubDBChecks(o Options) []Check {
	const id, name = "hubsrv.db", "hub 数据库"
	dbPath := filepath.Join(o.HubDir, "hub.db")
	skipRest := func(detail string) []Check {
		return []Check{
			{ID: "hubsrv.password", Name: "网页密码", Status: Skip, Detail: detail},
			{ID: "hubsrv.vapid", Name: "推送密钥", Status: Skip, Detail: detail},
		}
	}
	perm := permCheck(id, name, dbPath, 0o600, false, "railse hub run 会建好数据库")
	if _, err := os.Stat(dbPath); err != nil {
		return append([]Check{perm}, skipRest("没有数据库")...)
	}
	broken := "hub.db 损坏或不是 hub 的数据库：从备份恢复，或移走它让 railse hub run 新建（机器要重新接入）"
	db, err := openReadOnly(dbPath)
	if err != nil {
		return append([]Check{withPerm(perm, Check{ID: id, Name: name, Status: Fail, Detail: "打开失败：" + err.Error(), Hint: broken})}, skipRest("数据库打不开")...)
	}
	defer db.Close()
	var v int
	if err := db.QueryRow("SELECT v FROM schema_version").Scan(&v); err != nil {
		return append([]Check{withPerm(perm, Check{ID: id, Name: name, Status: Fail, Detail: "读不到 schema_version：" + err.Error(), Hint: broken})}, skipRest("数据库读不了")...)
	}
	var schema Check
	switch {
	case v > store.SchemaVersion:
		schema = Check{ID: id, Name: name, Status: Fail, Detail: fmt.Sprintf("库版本 %d 比程序（%d）新，hub 起不来", v, store.SchemaVersion), Hint: "升级 railse 到建这个库的版本或更新"}
	case v < store.SchemaVersion:
		schema = Check{ID: id, Name: name, Status: Warn, Detail: fmt.Sprintf("库版本 %d，程序 %d", v, store.SchemaVersion), Hint: "下次 railse hub run 会自动升级（建议先备份 hub.db）"}
	default:
		schema = Check{ID: id, Name: name, Status: OK, Detail: fmt.Sprintf("%s，版本 %d", dbPath, v)}
	}
	out := []Check{withPerm(perm, schema)}

	setting := func(key string) (string, bool, error) {
		var val string
		err := db.QueryRow("SELECT value FROM settings WHERE key = ?", key).Scan(&val)
		if errors.Is(err, sql.ErrNoRows) {
			return "", false, nil
		}
		return val, err == nil, err
	}
	switch _, ok, err := setting("password_hash"); {
	case err != nil:
		out = append(out, Check{ID: "hubsrv.password", Name: "网页密码", Status: Fail, Detail: "读 settings 出错：" + err.Error(), Hint: broken})
	case ok:
		out = append(out, Check{ID: "hubsrv.password", Name: "网页密码", Status: OK, Detail: "已设置"})
	default:
		out = append(out, Check{ID: "hubsrv.password", Name: "网页密码", Status: Warn, Detail: "还没设密码，网页登录会得到 503", Hint: "railse hub password"})
	}
	// 错误信息不带密钥内容
	switch raw, ok, err := setting("vapid_private"); {
	case err != nil:
		out = append(out, Check{ID: "hubsrv.vapid", Name: "推送密钥", Status: Fail, Detail: "读 settings 出错：" + err.Error(), Hint: broken})
	case !ok:
		out = append(out, Check{ID: "hubsrv.vapid", Name: "推送密钥", Status: Warn, Detail: "VAPID 密钥还没生成", Hint: "首次 railse hub run 会生成；也可以 railse hub vapid"})
	default:
		b, err := base64.RawURLEncoding.DecodeString(raw)
		if err == nil {
			_, err = push.ParseVAPID(b)
		}
		if err != nil {
			out = append(out, Check{ID: "hubsrv.vapid", Name: "推送密钥", Status: Fail, Detail: "VAPID 密钥损坏，hub 起不来", Hint: "railse hub vapid --rotate 重建（所有手机要重新开启通知）"})
		} else {
			out = append(out, Check{ID: "hubsrv.vapid", Name: "推送密钥", Status: OK, Detail: "VAPID 密钥可用"})
		}
	}
	return out
}

// withPerm 把 hub.db 的权限结论并进库的结论：权限没问题就只留库的结论；有问题（permCheck 只会给 fail）时整项 fail，
// 两边的细节与提示都留着。
func withPerm(perm, c Check) Check {
	if perm.Status == OK {
		return c
	}
	c.Status = Fail
	c.Detail = perm.Detail + "；" + c.Detail
	c.Hint = strings.TrimPrefix(c.Hint+"；"+perm.Hint, "；")
	return c
}

// acmeCheck 看 ACME 自动证书目录（账户与证书私钥）：目录 0700，里面的文件 0600。没有这个目录就是没用 ACME。
func acmeCheck(o Options) Check {
	const id, name = "hubsrv.acme", "ACME 证书目录"
	dir := filepath.Join(o.HubDir, "acme")
	if _, err := os.Stat(dir); errors.Is(err, fs.ErrNotExist) {
		return Check{ID: id, Name: name, Status: Skip, Detail: "没有 acme 目录（没用 --acme-domain 自动证书）"}
	}
	c := permCheck(id, name, dir, 0o700, true, "railse hub run --acme-domain … 会建好")
	if c.Status != OK {
		return c
	}
	ents, err := os.ReadDir(dir)
	if err != nil {
		return Check{ID: id, Name: name, Status: Fail, Detail: err.Error(), Hint: "确认用跑 hub 的同一个用户运行 doctor"}
	}
	var bad, badPaths []string
	for _, e := range ents {
		p := filepath.Join(dir, e.Name())
		st, err := os.Lstat(p)
		if err != nil || !st.Mode().IsRegular() {
			continue
		}
		if st.Mode().Perm() != 0o600 {
			bad = append(bad, fmt.Sprintf("%s(%04o)", e.Name(), st.Mode().Perm()))
			badPaths = append(badPaths, p)
		}
	}
	if len(bad) > 0 {
		return Check{ID: id, Name: name, Status: Fail, Detail: "里面有私钥文件不是 0600：" + strings.Join(bad, "、"), Hint: "chmod 600 " + strings.Join(badPaths, " ")}
	}
	return Check{ID: id, Name: name, Status: OK, Detail: dir}
}
