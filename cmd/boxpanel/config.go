package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/revfanc/boxpanel/internal/app"
)

type pathFlags struct {
	dataDir  string
	database string
	cert     string
	key      string
}

func addPathFlags(flags *flag.FlagSet) *pathFlags {
	p := new(pathFlags)
	flags.StringVar(&p.dataDir, "data-dir", os.Getenv("BOXPANEL_DATA_DIR"), "data directory (BOXPANEL_DATA_DIR; default /etc/boxpanel; dev defaults to current directory)")
	flags.StringVar(&p.database, "db-path", os.Getenv("BOXPANEL_DB_PATH"), "database file (BOXPANEL_DB_PATH; default DATA_DIR/boxpanel.db, or ./dev.db in dev mode)")
	flags.StringVar(&p.cert, "cert-file", os.Getenv("BOXPANEL_CERT_FILE"), "panel certificate (BOXPANEL_CERT_FILE; default DATA_DIR/certs/panel.crt)")
	flags.StringVar(&p.key, "key-file", os.Getenv("BOXPANEL_KEY_FILE"), "panel private key (BOXPANEL_KEY_FILE; default DATA_DIR/certs/panel.key)")
	return p
}

func (p pathFlags) resolve(dev bool) (app.Paths, error) {
	dir := p.dataDir
	dbName := "boxpanel.db"
	if dir == "" {
		dir = "/etc/boxpanel"
		if dev {
			dir, dbName = ".", "dev.db"
		}
	}
	paths := app.Paths{Database: p.database, CertFile: p.cert, KeyFile: p.key}
	if paths.Database == "" {
		paths.Database = filepath.Join(dir, dbName)
	}
	if paths.CertFile == "" {
		paths.CertFile = filepath.Join(dir, "certs", "panel.crt")
	}
	if paths.KeyFile == "" {
		paths.KeyFile = filepath.Join(dir, "certs", "panel.key")
	}
	return paths, paths.Validate()
}

func runConfigCommand(command string, args []string, stdout, stderr io.Writer) int {
	flags := flag.NewFlagSet(command, flag.ContinueOnError)
	flags.SetOutput(stdout)
	paths := addPathFlags(flags)
	dev := flags.Bool("dev", false, "use the local development database and HTTP settings")
	port := 0
	publicIP := ""
	if command == "init" {
		flags.IntVar(&port, "port", 0, "panel port (1-65535; omitted selects an available random port 10000-60000)")
		flags.StringVar(&publicIP, "public-ip", os.Getenv("BOXPANEL_PUBLIC_IP"), "server IP; skip network detection (BOXPANEL_PUBLIC_IP; use 127.0.0.1 for local testing)")
	}
	if err := flags.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0
		}
		fmt.Fprintf(stderr, "boxpanel: %v; run 'boxpanel %s --help' for usage\n", err, command)
		return 2
	}
	invalidPort := false
	flags.Visit(func(f *flag.Flag) {
		if f.Name == "port" && (port < 1 || port > 65535) {
			invalidPort = true
		}
	})
	if flags.NArg() != 0 || invalidPort {
		fmt.Fprintf(stderr, "boxpanel: invalid arguments; run 'boxpanel %s --help' for usage\n", command)
		return 2
	}
	resolved, err := paths.resolve(*dev)
	if err == nil {
		err = configCommand(context.Background(), command, app.InitOptions{Paths: resolved, Port: port, PublicIP: publicIP, Dev: *dev}, stdout)
	}
	if err != nil {
		fmt.Fprintf(stderr, "boxpanel: %v; 请检查数据路径与权限，再按 'boxpanel %s --help' 重试。\n", err, command)
		return 1
	}
	return 0
}

func configCommand(ctx context.Context, command string, opts app.InitOptions, stdout io.Writer) error {
	if command == "init" {
		result, err := app.Initialize(ctx, opts)
		if err != nil {
			return err
		}
		printInitialized(stdout, result)
		return nil
	}
	conn, err := app.OpenExisting(ctx, opts.Paths.Database)
	if err != nil {
		return err
	}
	defer conn.Close()
	if command == "reset-admin" {
		credentials, err := app.ResetAdmin(ctx, conn)
		if err != nil {
			return err
		}
		fmt.Fprintln(stdout, "管理员已重置，旧会话已失效。请保存以下新凭证；忘记时可再次执行 reset-admin。")
		printCredentials(stdout, credentials)
		return nil
	}
	cfg, err := app.LoadConfig(ctx, conn)
	if err != nil {
		return err
	}
	username, err := app.AdminUsername(ctx, conn)
	if err != nil {
		return err
	}
	fmt.Fprintf(stdout, "面板地址: %s\n用户名:   %s\n", cfg.URL(), username)
	return nil
}

func printCredentials(stdout io.Writer, credentials app.Credentials) {
	fmt.Fprintf(stdout, "用户名:   %s\n密码:     %s\n", credentials.Username, credentials.Password)
}

func printInitialized(stdout io.Writer, result app.InitResult) {
	fmt.Fprintf(stdout, "boxpanel 初始化完成\n面板地址: %s\n", result.URL())
	printCredentials(stdout, result.Credentials)
	fmt.Fprintln(stdout, "请保存凭证；密码仅在初始化或重置时显示。忘记时可执行 boxpanel reset-admin。")
	if result.TLSMode == "self_signed" {
		fmt.Fprintln(stdout, "提示: 浏览器提示不安全是因为使用了自签证书，请核对面板地址后选择高级 → 继续访问。")
		fmt.Fprintf(stdout, "请确认云服务商安全组已放行 TCP %d。\n", result.WebPort)
	}
}
