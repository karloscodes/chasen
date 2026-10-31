# Intentionally old base. Node itself is the upstream tarball in /usr/local, not a Debian package.
FROM node:20.11.1-bookworm-slim

WORKDIR /app
COPY package.json package-lock.json ./
RUN npm ci --omit=dev
COPY server.js ./

EXPOSE 3000
CMD ["node", "server.js"]
