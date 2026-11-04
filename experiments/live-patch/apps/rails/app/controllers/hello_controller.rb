class HelloController < ActionController::API
  def index
    render plain: "rails #{Rails.version}\n"
  end
end
