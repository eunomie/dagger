#!/usr/bin/env ruby

require 'dagger'
require_relative './dagger'

def mount_cache(c)
  node_cache = @dag.cache_volume(key: "node")
  c.with_mounted_cache(cache: node_cache, path: "/root/.npm")
end

class HelloDagger
  def hello_world(str)
    @dag
      .container
      .from(address: "alpine:latest")
      .with_exec(args: ["echo", str])
  end

  def grep_dir(dir, pattern)
    mount_dir = @dag
      .host
      .directory(path: dir)
    @dag
      .container
      .from(address: "alpine:latest")
      .with_mounted_directory(path: "/mnt", directory: mount_dir)
      .with_workdir(path: "/mnt")
      .with_exec(args: ["grep", "-R", pattern, "."])
      .stdout
  end
end
