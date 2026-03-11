Emby 媒体服务器流量控制中间件，提供用户级限速、客户端限速、服务端限速和流量统计功能。

## 功能特性

- **用户级限速**: 为不同用户设置独立的上下行带宽限制
- **客户端限速**: 按 User-Agent 识别客户端并应用统一限速规则
- **服务端限速**: 控制后端服务器的总带宽分配
- **Web 管理面板**: 直观的界面配置所有规则
- **流量统计**: 记录并展示用户、客户端和数据库明细流量
- **数据库记录搜索与批量删除**: 可快速筛选并清理未知用户、异常客户端或指定路径数据

## 系统架构

```
┌─────────────────────────────────────────────────────────────┐
│                        客户端请求                            │
└─────────────────────────┬───────────────────────────────────┘
                          ▼
┌─────────────────────────────────────────────────────────────┐
│              emby-media-portal (Go 中间件)                   │
│  ┌─────────────┬─────────────┬─────────────┬─────────────┐  │
│  │ 用户识别     │ 限速引擎     │ 客户端规则   │ 统计记录    │  │
│  └─────────────┴─────────────┴─────────────┴─────────────┘  │
│                         │                                    │
│  ┌──────────────────────┴────────────────────────────────┐  │
│  │                    管理面板 (Web UI)                    │  │
│  └────────────────────────────────────────────────────────┘  │
└─────────────────────────┬───────────────────────────────────┘
                          ▼
┌─────────────────────────────────────────────────────────────┐
│              Lucky / Emby Server                             │
└─────────────────────────────────────────────────────────────┘
```

## 快速开始

### 1. 编译

```bash
cd emby-media-portal
go build -o emby-media-portal ./cmd/server
```

### 2. 配置

复制示例配置并编辑本地 `config.yaml`：

```bash
cp config.example.yaml config.yaml
```

然后修改 `config.yaml`：

```yaml
server:
  listen: ":8095"
  admin_path: "/admin"
  admin_token: "your-secure-admin-token"

emby:
  url: "http://localhost:8096"
  api_key: "your-emby-api-key"

backend:
  type: "direct"  # Lucky 作为前置代理时，这里应保持 direct，让本程序直接转发到 Emby
  lucky_url: "http://localhost:16666"
  server_id: "emby-main"

rate_limits:
  default_upload: 0      # 0=无限
  default_download: 0
  global_limit: 0        # 全局带宽限制

database:
  path: "./data/config.db"
```

### 3. 运行

```bash
./emby-media-portal
```

### 4. 访问管理面板

打开浏览器访问 `http://localhost:8095/admin/`，如果你修改了 `server.listen` 或 `server.admin_path`，这里对应替换成新的端口和入口路径。

## API 文档

### 认证

所有 API 请求需要在 `Authorization` 头中携带管理令牌：

```
Authorization: your-admin-token
```

### 用户管理

| 方法 | 路径 | 描述 |
|------|------|------|
| GET | /api/users | 获取所有用户规则 |
| POST | /api/users/sync | 从 Emby 同步用户 |
| GET | /api/users/:id | 获取用户规则 |
| PUT | /api/users/:id | 更新用户规则 |
| DELETE | /api/users/:id | 删除用户规则 |

### 规则管理

| 方法 | 路径 | 描述 |
|------|------|------|
| GET | /api/rules/defaults | 获取默认限速设置 |
| PUT | /api/rules/defaults | 更新默认限速设置 |
| GET | /api/rules/servers | 获取所有服务器规则 |
| POST | /api/rules/servers | 创建服务器规则 |
| GET | /api/rules/servers/:id | 获取服务器规则 |
| DELETE | /api/rules/servers/:id | 删除服务器规则 |

### 流量统计

| 方法 | 路径 | 描述 |
|------|------|------|
| GET | /api/traffic/summary | 获取整体流量汇总 |
| GET | /api/traffic/users | 获取所有用户流量统计 |
| GET | /api/traffic/users/:id | 获取指定用户流量统计 |
| GET | /api/traffic/clients | 获取所有客户端流量统计 |
| GET | /api/traffic/clients/:id | 获取指定客户端流量统计 |
| GET | /api/traffic/records | 获取数据库流量记录，支持分页和搜索 |
| DELETE | /api/traffic/records | 批量删除指定记录或按搜索条件删除 |
| DELETE | /api/traffic/records/:id | 删除单条数据库记录 |
| GET | /api/traffic/servers/:id | 获取指定服务器流量统计 |
| DELETE | /api/traffic/clean | 清理旧统计数据 |

## 限速单位说明

限速值以 **bytes/秒** 为单位：

- `0` = 无限制
- `1048576` = 1 MB/s
- `5242880` = 5 MB/s
- `10485760` = 10 MB/s

## 与 Lucky 反向代理集成

推荐使用串联模式：

```
Client → Lucky (:16666) → Emby-FC (:8095) → Emby (:8096)
```

Lucky 作为入口反向代理，把请求转发给 Emby-FC；Emby-FC 在应用限速和统计后，再转发到 Emby。

## 部署方式

### 直接运行

```bash
./emby-media-portal
```

### Docker 拉取

镜像发布后，用户可以直接拉取：

```bash
docker pull ghcr.io/rudy-yang/emby-media-portal:latest
```

或在 Compose 中直接使用：

```yaml
services:
  emby-media-portal:
    image: ghcr.io/rudy-yang/emby-media-portal:latest
    container_name: emby-media-portal
    restart: unless-stopped
    ports:
      - "8095:8095"
    volumes:
      - ./data:/app/data
      - ./config.yaml:/app/config.yaml
```

### Docker 部署

```dockerfile
FROM golang:1.21-alpine AS builder
WORKDIR /app
COPY . .
RUN go build -o emby-media-portal ./cmd/server

FROM alpine:latest
WORKDIR /app
COPY --from=builder /app/emby-media-portal .
COPY config.example.yaml ./config.yaml
EXPOSE 8095
CMD ["./emby-media-portal"]
```

```bash
docker build -t emby-media-portal .
docker run -d -p 8095:8095 -v ./data:/app/data emby-media-portal
```

### 自动发布到 GHCR

仓库已包含 GitHub Actions 工作流：

- 文件：`.github/workflows/docker-publish.yml`
- 推送到 `main` 时自动发布
- 打 tag（如 `v1.0.0`）时自动发布
- 发布地址：`ghcr.io/rudy-yang/emby-media-portal`

常用标签包括：

- `latest`
- `main`
- `vX.Y.Z`
- `sha-...`

### Systemd 服务 (Linux)

创建 `/etc/systemd/system/emby-media-portal.service`：

```ini
[Unit]
Description=emby-media-portal
After=network.target

[Service]
Type=simple
User=emby
WorkingDirectory=/opt/emby-media-portal
ExecStart=/opt/emby-media-portal/emby-media-portal
Restart=on-failure

[Install]
WantedBy=multi-user.target
```

```bash
systemctl enable emby-media-portal
systemctl start emby-media-portal
```

## 技术栈

- **Go 1.21+**
- **Gin** - HTTP 框架
- **SQLite** - 数据存储
- **golang.org/x/time/rate** - Token Bucket 限速算法

## 许可证

MIT License
