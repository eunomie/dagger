# frozen_string_literal: true

require_relative 'main'

# This is the main class of the Dagger Ruby SDK.
class HelloDagger
  def initialize
    @dag = Dagger.connect
  end

  def run(args)
    args.each do |arg|
      puts send(arg.gsub('-', '_'))
    end
  end
end

HelloDagger.new.run ARGV
