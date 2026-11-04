require_relative "config/environment"
require "rackup/handler/webrick"
require "webrick"

# Rack 3 moved the handlers into the rackup gem.
Rackup::Handler::WEBrick.run(
  Rails.application,
  Host: "0.0.0.0",
  Port: 3000,
  AccessLog: [],
  Logger: WEBrick::Log.new($stderr, WEBrick::BasicLog::WARN)
)
