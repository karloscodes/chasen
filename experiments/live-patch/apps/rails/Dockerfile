# Intentionally old base. The compiler stage uses the Debian snapshot from the
# week this Ruby image was published, so the gems are built against that glibc.
# The final stage does not run apt, and the patch scripts are what upgrade it.
FROM ruby:3.2.4-slim-bookworm AS build

WORKDIR /app
RUN set -eux; \
    rm -rf /etc/apt/sources.list.d; \
    mkdir -p /etc/apt/sources.list.d; \
    printf '%s\n' \
      'deb http://snapshot.debian.org/archive/debian/20240626T000000Z bookworm main' \
      'deb http://snapshot.debian.org/archive/debian-security/20240626T000000Z bookworm-security main' \
      > /etc/apt/sources.list; \
    apt-get -o Acquire::Check-Valid-Until=false update; \
    apt-get install -y --no-install-recommends --no-upgrade build-essential pkg-config libyaml-dev; \
    rm -rf /var/lib/apt/lists/*

ENV BUNDLE_WITHOUT=development:test \
    BUNDLE_PATH=/usr/local/bundle \
    BUNDLE_DEPLOYMENT=1

COPY Gemfile Gemfile.lock ./
RUN bundle install && rm -rf /usr/local/bundle/cache

FROM ruby:3.2.4-slim-bookworm

WORKDIR /app
ENV RAILS_ENV=production \
    BUNDLE_WITHOUT=development:test \
    BUNDLE_PATH=/usr/local/bundle \
    BUNDLE_DEPLOYMENT=1

COPY --from=build /usr/local/bundle /usr/local/bundle
COPY . .
RUN mkdir -p tmp/pids tmp/cache log

EXPOSE 3000
CMD ["bundle", "exec", "ruby", "server.rb"]
