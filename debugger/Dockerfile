FROM node:24-alpine AS build
# Vite/rollup hold the whole module graph in one Node heap: on a shared host
# cap it (default 4 GiB is plenty for this app; lower it with
# --build-arg NPM_MAX_OLD_SPACE=2048 when building next to a busy stack).
ARG NPM_MAX_OLD_SPACE=4096
ENV NODE_OPTIONS=--max-old-space-size=${NPM_MAX_OLD_SPACE}
WORKDIR /app
COPY package.json package-lock.json ./
# npm cache mount (BuildKit): dependency installs reuse the downloaded tarball
# cache across builds instead of re-fetching every package.
RUN --mount=type=cache,target=/root/.npm \
    npm install --no-audit --no-fund
COPY . .
RUN npm run build

FROM nginx:1.29-alpine
COPY nginx.conf /etc/nginx/conf.d/default.conf
COPY --from=build /app/dist /usr/share/nginx/html
EXPOSE 80