# Builds the web app with Bun and serves the files with nginx. The build
# context is the repo root, because the app imports the generated client in
# gen/ts and installs through the root Bun workspace.
FROM oven/bun:1.4.4-alpine AS build
WORKDIR /src
COPY package.json bun.lock bunfig.toml ./
COPY services/web/package.json services/web/
RUN bun install --frozen-lockfile
COPY gen/ts gen/ts
COPY services/web services/web
# Where the browser reaches the gateway. It's baked into the build.
ARG VITE_GATEWAY_URL=http://localhost:8080
ENV VITE_GATEWAY_URL=$VITE_GATEWAY_URL
RUN cd services/web && bun run build

FROM nginx:1.30.6-alpine
COPY services/web/nginx.conf /etc/nginx/conf.d/default.conf
COPY --from=build /src/services/web/dist /usr/share/nginx/html
