require_relative "boot"

require "rails"
require "action_controller/railtie"
require "action_view/railtie"

# A production boot of Rails, small enough to build without a database.
# The key is a fixed placeholder so the process can boot. It is not a credential.
module LivePatch
  class Application < Rails::Application
    config.load_defaults 7.1
    config.api_only = true
    config.eager_load = true
    # Fixed placeholder so production mode can boot. Not a credential.
    config.secret_key_base = "0" * 128
    config.hosts.clear
    config.consider_all_requests_local = false
    config.log_level = :info
    config.logger = ActiveSupport::Logger.new($stdout)
  end
end
