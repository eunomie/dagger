# frozen_string_literal: true

Gem::Specification.new do |s|
  s.name = 'dagger'
  s.version = '0.0.1'
  s.date = '2024-10-02'
  s.summary = 'Dagger'
  s.description = 'An engine to run your pipelines in containers'
  s.authors = ['Yves Brissaud']
  s.email = 'yves.brissaud@gmail.com'
  all_files = `git ls-files -z`.split("\x0")
  s.files = all_files.grep(%r{^sdk/ruby/(bin|lib)/})
  s.require_paths = ['lib']
  s.homepage = 'https://dagger.io'
  s.license = 'Apache-2.0 license'
  s.required_ruby_version = '>= 3.3.0'
end
