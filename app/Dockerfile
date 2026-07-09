FROM node:18-alpine
WORKDIR /app
COPY package.json ./
RUN npm install
COPY src ./src
COPY tests ./tests
ENV PORT=8080
EXPOSE 8080
CMD ["npm", "start"]
