# ===== 阶段 1：构建前端（Vite） =====
FROM node:24-alpine AS webbuilder
WORKDIR /web
COPY web/package.json web/package-lock.json ./
RUN npm ci || npm install
COPY web/ ./
RUN npm run build

# ===== 阶段 2：构建后端 =====
FROM golang:1.26-alpine AS builder

WORKDIR /build

# 安装依赖
RUN apk add --no-cache git make

# 复制 go.mod 和 go.sum
COPY go.mod go.sum ./

# 下载依赖（如果网络可用）
RUN go mod download || true

# 复制源代码
COPY . .

# 编译
RUN CGO_ENABLED=0 GOOS=linux go build -o gateway ./cmd/gateway

# ===== 阶段 3：运行时 =====
FROM alpine:latest

WORKDIR /app

# 安装 CA 证书（HTTPS 调用需要）
RUN apk --no-cache add ca-certificates tzdata

# 复制编译好的二进制文件
COPY --from=builder /build/gateway .

# 复制配置文件
COPY configs ./configs

# 复制前端构建产物（Gateway 同端口静态托管）
COPY --from=webbuilder /web/dist ./web/dist

EXPOSE 8080

CMD ["./gateway"]
