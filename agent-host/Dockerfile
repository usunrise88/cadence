FROM node:22-slim
ARG CADENCE_VERSION=dev
LABEL org.opencontainers.image.version=$CADENCE_VERSION
WORKDIR /app
COPY package.json package-lock.json ./
RUN npm ci --omit=dev --ignore-scripts=false
COPY src ./src
COPY tsconfig.json ./
RUN npm install --no-save tsx@4.23.15
ENV CADENCE_VERSION=$CADENCE_VERSION
USER node
CMD ["npx", "tsx", "src/index.ts"]
