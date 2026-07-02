# 前端构建阶段
FROM --platform=$BUILDPLATFORM node:18 AS frontend-builder
WORKDIR /app
# 复制 excalidraw 子模块
COPY excalidraw/ ./excalidraw/
# Self-hosted patch: persist room files (images) on our own backend instead of
# Firebase Storage. Applied at build time so the submodule stays pristine.
# `git apply` fails the build loudly if the submodule is bumped and the patch no
# longer applies — that is intentional (keep the patch stable).
COPY room-files-frontend.patch ./
RUN cd excalidraw && git apply ../room-files-frontend.patch
# 构建前端
RUN cd excalidraw && npm install -g pnpm && pnpm install && cd excalidraw-app && DISABLE_VITE_CHECKER=true pnpm build:app:docker

# 后端构建阶段
FROM --platform=$BUILDPLATFORM golang:alpine AS backend-builder
RUN apk update && apk add --no-cache git
WORKDIR /app
ARG TARGETOS
ARG TARGETARCH
# 复制 Go 模块文件
COPY go.mod go.sum ./
RUN go mod download
# 复制源代码
COPY . .
# 复制前端构建文件到正确位置，以便 Go embed 可以找到
COPY --from=frontend-builder /app/excalidraw/excalidraw-app/build ./frontend/
# 构建 Go 应用
RUN CGO_ENABLED=0 GOOS=$TARGETOS GOARCH=$TARGETARCH go build -ldflags="-s -w" -o main .

# 最终运行镜像
FROM --platform=$TARGETPLATFORM alpine:latest
RUN apk --no-cache add ca-certificates
WORKDIR /root/
# 复制后端二进制文件（已包含嵌入的前端文件）
COPY --from=backend-builder /app/main .
# 暴露端口
EXPOSE 3002
CMD ["./main"]
