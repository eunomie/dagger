#!/usr/bin/env ruby

# require 'bundler/inline'

# gemfile do
#     load("#{File.dirname(__FILE__)}/Gemfile")
#   source 'https://rubygems.org'
#   gem 'base64', '~> 0.2.0'
# end

$LOAD_PATH.unshift("#{File.dirname(__FILE__)}/sdk", "#{File.dirname(__FILE__)}/lib")

# require 'dagger_module'
require 'dagger'

# def run(args)
#   name = args.shift
#   puts DaggerModule.new.send(name.gsub('-', '_'), *args)
# end

def invoke
  dag = Dagger.connect
  dag
    .module_
    .with_object(
      object: dag
                .type_def
                .with_object(name: "Ruby")
                .with_function(
                  function: dag
                              .function(
                                name: "container-hello",
                                return_type: dag
                                               .type_def
                                               .with_object(name: "container"))
                              .with_arg(
                                name: "string-arg",
                                type_def: dag.type_def.with_scalar(name: "String"))))
end

def dispatch
  dag = Dagger.connect

  fn_call = dag.current_function_call
  # parent_name = fn_call.parent_name
  # fn_name = fn_call.name
  # parent_json = fn_call.parent
  fn_args = fn_call.input_args

  input_args = {}
  fn_args.each do |arg|
    arg_name = arg.name
    arg_value = arg.value
    input_args[arg_name] = arg_value
  end

  res = dag.client.invoke(invoke) #(parent_json, parent_name, fn_name, input_args)
  fn_call.return_value(value: res.to_json)
end

def main
#   if ARGV.empty?
    dispatch
#   else
#     run(ARGV)
#   end
end

main
