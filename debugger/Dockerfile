FROM node:24-alpine AS build
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