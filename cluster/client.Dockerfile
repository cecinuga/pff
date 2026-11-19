# syntax=docker/dockerfile:1
# Web client image. Build context: ../client (see docker-compose.yml).
# The bundle has no environment-specific values: Firebase settings are fetched
# at runtime from /api/v1/public/config.

FROM node:22-alpine AS build
WORKDIR /app
COPY package.json package-lock.json ./
RUN npm ci --no-audit --no-fund
COPY . .
RUN npm run build

# Unprivileged nginx (listens on 8080, runs as a non-root user).
FROM nginxinc/nginx-unprivileged:1.27-alpine
COPY --from=build /app/dist /usr/share/nginx/html
EXPOSE 8080
