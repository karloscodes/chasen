Rails.application.routes.draw do
  get "/up", to: proc { |_env| [200, {"Content-Type" => "text/plain"}, ["ok\n"]] }
  get "/", to: "hello#index"
end
