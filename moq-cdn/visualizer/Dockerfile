FROM node:22-alpine AS build

WORKDIR /src
COPY package.json package-lock.json build.mjs ./
RUN npm ci
COPY . ./
RUN MOQ_VISUALIZER_OUTDIR=/out/vendor npm run build \
    && cp index.html app.js bootstrap.js rewind.js style.css \
      distribution.html distribution.js distribution.css /out/

FROM nginx:1.29-alpine
COPY --from=build /out /usr/share/nginx/html
