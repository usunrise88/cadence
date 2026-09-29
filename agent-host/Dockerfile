FROM node:22
WORKDIR /app
COPY package.json ./
RUN npm install
COPY src ./src
CMD ["npm", "start"]
