# Linux 主机安装与升级

使用 `make build` 和 `make bundle` 生成与主机架构匹配的 Go Linux 发布包。发布包包含管理程序、同一版本的 Web 文件、运维工具以及 `deploy/systemd/` 中的服务和账号声明。容器部署可使用仓库的 `compose.yaml`。 [构建说明](../README.md)

## 安装服务

在支持 `systemd-sysusers` 的 Linux 主机上，以管理员权限安装已审阅的发布包。示例使用独立发布目录，`/opt/moedove-ixp-manager` 是服务使用的固定链接。按发布版本替换示例中的 `r1`。

```sh
(
set -eu
release=/opt/moedove-ixp-manager-r1
sudo mkdir -m 0755 "$release"
sudo tar --no-same-owner -xzf ./moedove-ixp-manager.tar.gz --strip-components=1 -C "$release"
sudo ln -s "$release" /opt/moedove-ixp-manager

sudo install -m 0644 "$release/deploy/systemd/moedove-ixp-manager.sysusers.conf" \
  /etc/sysusers.d/moedove-ixp-manager.conf
sudo systemd-sysusers /etc/sysusers.d/moedove-ixp-manager.conf
sudo install -m 0644 "$release/deploy/systemd/moedove-ixp-manager.service" \
  /etc/systemd/system/moedove-ixp-manager.service
sudo systemd-analyze verify /etc/systemd/system/moedove-ixp-manager.service
sudo systemctl daemon-reload
sudo systemctl enable --now moedove-ixp-manager.service
)
```

服务使用专用 `moedove-ixp-manager` 账号。systemd 管理权限为 `0700` 的持久数据目录 `/var/lib/moedove-ixp-manager` 和运行目录 `/run/moedove-ixp-manager`。二进制从实际发布目录读取相邻的 `web/`，程序文件由管理员管理。

服务监听 `127.0.0.1:8080` 并启用安全 Cookie。让 HTTPS 反向代理转发到该回环地址并保留浏览器请求的 `Host`，然后通过 HTTPS 入口完成初始化。`curl -fsS http://127.0.0.1:8080/healthz` 可检查本机 HTTP 存活。

首次初始化使用启动日志中的设置令牌。通过 `sudo journalctl -u moedove-ixp-manager.service -n 50` 在主机上查看。也可以提前在仅 root 可读的 `/etc/moedove-ixp-manager/manager.env` 中设置 `IXP_SETUP_TOKEN`。初始化完成后可移除该文件中的令牌。保护启动日志和配置文件。

## 原生检查器与配置

按使用的网络平台安装相应的原生检查器。服务文件中的 FRR 路径为 `/usr/lib/frr/bgpd`，OpenBGPD 路径为 `/usr/sbin/bgpd`。其他检查器沿用管理程序的默认命令和系统路径。FRR 和 Quagga 检查需要平台允许专用服务账号使用相应的非特权命名空间。

通过 `sudo systemctl edit moedove-ixp-manager.service` 增加本机参数。替换 `ExecStart` 时先用空的 `ExecStart=` 清除旧设置，并保留数据目录、回环监听及安全 Cookie 参数。供应商、邮件和 Atlas 配置使用对应的 Go CLI 路径参数。将这些私有配置文件设为 `0600` 并交给专用服务账号，所在目录由 root 管理并允许服务账号访问。

## 更新与恢复

将新版解压到另一个 root 管理的版本目录。更新前停止服务，在仅服务账号可访问的目录保留备份，用新版工具验证归档。

```sh
account=moedove-ixp-manager
current=/opt/moedove-ixp-manager
new=/opt/moedove-ixp-manager-r2
state=/var/lib/moedove-ixp-manager
backup_dir=/var/backups/moedove-ixp-manager
backup="$backup_dir/before-r2.ixp-backup.tar.gz"

sudo systemctl stop moedove-ixp-manager.service
sudo install -d -o "$account" -g "$account" -m 0700 "$backup_dir"
sudo -u "$account" "$current/ixp-backup" create --data "$state" --output "$backup"
sudo -u "$account" "$new/ixp-backup" verify --input "$backup" --work-dir "$backup_dir"
```

首次升级可先恢复到新目录，确认新的程序能够启动及登录。恢复工具要求目标路径尚不存在。以 root 恢复后，把新目录交给专用服务账号。在核对副本时使用单独的回环端口。

```sh
candidate=/var/lib/moedove-ixp-manager-r2-rehearsal
sudo "$new/ixp-backup" restore --input "$backup" --data "$candidate"
sudo chown -R "$account:$account" "$candidate"
sudo -u "$account" "$new/ixp-manager" -listen=127.0.0.1:18080 -data="$candidate"
```

结束副本演练后，可在主服务停止期间切换已核对的二进制链接，并重新启动服务。原数据目录继续使用既有路径。

```sh
test -L "$current" && sudo ln -s "$new" "$current.next" &&
  sudo mv -Tf "$current.next" "$current" && sudo systemctl start moedove-ixp-manager.service
```

恢复正式数据时，先保留当前数据目录和备份，再把恢复并核对属主的新目录切换到服务的状态路径。回退应用版本时使用该版本对应的备份状态，另行核对切换后产生的业务变更。备份包含密钥和私有数据，存放在限权位置。
