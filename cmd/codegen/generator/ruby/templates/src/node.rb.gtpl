{{ define "node" -}}
{{ "  " }}# Node class, base class for all objects.
  class Node
    def initialize(parent, client, name, args = {})
      @parent = parent
      @parent&.set_child(self)
      @client = client
      @name = name
      @args = args
      @child = nil
    end

    def to_s
      s = str
      n = self
      until n.parent.nil?
        n = n.parent
        s = n.str + "{\n#{s}\n}"
      end
      s
    end

    def value(res)
      if res.key?('errors') && !res['errors'].empty?
        puts res['errors'].collect { |e| e['message'] }.join("\n")
        exit(false)
      end

      keys = [@name]
      n = @parent
      until n.nil?
        keys.unshift(n.name) unless n.name.empty?
        n = n.parent
      end

      keys.inject(res['data']) { |el, key| el[key] }
    end

    protected

    attr_reader :parent, :name

    def set_child(child) # rubocop:disable Naming/AccessorMethodName
      @child = child
    end

    def str
      s = String.new(@name)
      unless @args.empty?
        s << '('
        s << @args.map { |k, v| "#{k}:#{arg_str(v)}" }.join(', ')
        s << ')'
      end
      s
    end

    def arg_str(value)
      case value
      when String
        "\"#{value}\""
      when Numeric
        value.to_s
      when Array
        "[#{value.map { |v| arg_str(v) }.join(', ')}]"
      when Hash
        "{ #{value.map { |k, v| "#{k}: #{arg_str(v)}" }.join(', ')} }"
      else
        "\"#{value.id}\""
      end
    end

    def assert_not_nil(name, value)
      return unless value.nil?

      warn("#{name} cannot be nil")
      exit(false)
    end
  end
{{ end }}